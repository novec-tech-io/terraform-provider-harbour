package provider

// Tests for the harbour_certificate_renew action (KAN-168) and the client
// call behind it, against the same httptest fake of the Harbour API used by
// the renewal-chain tests.

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// ---- client: RenewCertificate ----------------------------------------------

func TestRenewCertificate_Success(t *testing.T) {
	f := newFakeHarbour(chain("a")...)
	got, err := testClient(t, f).RenewCertificate(context.Background(), "a")
	if err != nil || got.OldRequestID != "a" || got.NewRequestID != "a-r" || got.Certificate.Status != "issued" {
		t.Fatalf("got %+v, err %v", got, err)
	}
	if got.Certificate.ACMCertificateARN != "arn:acm:stable" {
		t.Fatalf("the renewal must report the same ACM ARN, got %q", got.Certificate.ACMCertificateARN)
	}
}

func TestRenewCertificate_ErrorMapping(t *testing.T) {
	ctx := context.Background()
	if _, err := testClient(t, newFakeHarbour()).RenewCertificate(ctx, "nope"); err == nil {
		t.Fatal("unknown id must error")
	} else if _, ok := err.(*NotFoundError); !ok {
		t.Fatalf("expected *NotFoundError, got %T", err)
	}

	superseded := newFakeHarbour(chain("a", "b")...)
	if _, err := testClient(t, superseded).RenewCertificate(ctx, "a"); err == nil {
		t.Fatal("renewing a superseded record must error")
	} else if _, ok := err.(*ConflictError); !ok {
		t.Fatalf("expected *ConflictError, got %T", err)
	}

	for _, code := range []int{429, 502} {
		f := newFakeHarbour(chain("a")...)
		f.renewStatus = code
		_, err := testClient(t, f).RenewCertificate(ctx, "a")
		if err == nil || !strings.Contains(err.Error(), "forced failure") {
			t.Fatalf("status %d: expected the API's message in the error, got %v", code, err)
		}
	}
}

// ---- action ----------------------------------------------------------------

func renewRequest(t *testing.T, a *CertificateRenewAction, requestID string) action.InvokeRequest {
	t.Helper()
	ctx := context.Background()
	var sr action.SchemaResponse
	a.Schema(ctx, action.SchemaRequest{}, &sr)
	raw := tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{
		"request_id": tftypes.NewValue(tftypes.String, requestID),
	})
	return action.InvokeRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: raw}}
}

func invokeRenew(t *testing.T, f *fakeHarbour, requestID string) (*action.InvokeResponse, []string) {
	t.Helper()
	a := &CertificateRenewAction{client: testClient(t, f)}
	var msgs []string
	resp := &action.InvokeResponse{SendProgress: func(e action.InvokeProgressEvent) { msgs = append(msgs, e.Message) }}
	a.Invoke(context.Background(), renewRequest(t, a, requestID), resp)
	return resp, msgs
}

func TestRenewAction_RenewsAnIssuedCertificate(t *testing.T) {
	f := newFakeHarbour(chain("a")...)
	resp, msgs := invokeRenew(t, f, "a")
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
	if len(f.renewCalls) != 1 || f.renewCalls[0] != "a" {
		t.Fatalf("expected exactly one renew call on a, got %v", f.renewCalls)
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{"Renewing svc.example.test", "a -> a-r", "arn:acm:stable"} {
		if !strings.Contains(all, want) {
			t.Fatalf("progress output is missing %q:\n%s", want, all)
		}
	}
}

// The action's config usually comes from state that is behind a renewal. It
// must renew the LIVE certificate, not fail on the superseded one.
func TestRenewAction_ResolvesAStaleRequestIDToTheLiveCertificate(t *testing.T) {
	f := newFakeHarbour(chain("old", "live")...)
	resp, _ := invokeRenew(t, f, "old")
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
	if len(f.renewCalls) != 1 || f.renewCalls[0] != "live" {
		t.Fatalf("expected the renew call on the live record, got %v", f.renewCalls)
	}
}

func TestRenewAction_UnknownCertificateIsAClearError(t *testing.T) {
	f := newFakeHarbour()
	resp, _ := invokeRenew(t, f, "ghost")
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "not found") {
		t.Fatalf("expected a 'not found' error, got %v", resp.Diagnostics)
	}
	if len(f.renewCalls) != 0 {
		t.Fatalf("must not attempt a renewal, got %v", f.renewCalls)
	}
}

func TestRenewAction_ServerRefusalIsSurfacedNotSwallowed(t *testing.T) {
	// A record that ended revoked: the server answers 409 and the user must see why.
	recs := chain("a")
	recs[0].Status = "revoked"
	resp, _ := invokeRenew(t, newFakeHarbour(recs...), "a")
	if !resp.Diagnostics.HasError() {
		t.Fatal("renewing a revoked certificate must be an error")
	}
	d := resp.Diagnostics.Errors()[0]
	if d.Summary() != "Certificate cannot be renewed" || !strings.Contains(d.Detail(), "revoked") {
		t.Fatalf("got %q / %q", d.Summary(), d.Detail())
	}
}

func TestRenewAction_CooldownAndFailureAreErrors(t *testing.T) {
	want := map[int]string{429: "Renewal not allowed right now", 502: "Renewal failed", 500: "Failed to renew the certificate"}
	for code, summary := range want {
		f := newFakeHarbour(chain("a")...)
		f.renewStatus = code
		resp, _ := invokeRenew(t, f, "a")
		if !resp.Diagnostics.HasError() {
			t.Fatalf("status %d must fail the action", code)
		}
		d := resp.Diagnostics.Errors()[0]
		if d.Summary() != summary {
			t.Fatalf("status %d: summary = %q, want %q", code, d.Summary(), summary)
		}
		// The customer sees the API's own sentence, not raw JSON.
		if !strings.Contains(d.Detail(), "forced failure") || strings.ContainsAny(d.Detail(), "{}") {
			t.Fatalf("status %d: detail should be the plain message, got %q", code, d.Detail())
		}
	}
}

func TestRenewAction_FailureTellsTheUserTheCertificateIsUnchanged(t *testing.T) {
	f := newFakeHarbour(chain("a")...)
	f.renewStatus = 502
	resp, _ := invokeRenew(t, f, "a")
	if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "unchanged") {
		t.Fatalf("a failed renewal must say the existing certificate is unchanged: %q", resp.Diagnostics.Errors()[0].Detail())
	}
}

func TestAPIError_KeepsItsLegacyTextAndExtractsMessages(t *testing.T) {
	e := &APIError{Status: 429, Body: `{"errors": ["slow down", "really"]}`}
	if e.Error() != `harbour API returned 429: {"errors": ["slow down", "really"]}` {
		t.Fatalf("Error() text changed: %q", e.Error())
	}
	if e.Messages() != "slow down; really" {
		t.Fatalf("Messages() = %q", e.Messages())
	}
	if (&APIError{Status: 500, Body: "plain text"}).Messages() != "plain text" {
		t.Fatal("a non-JSON body must pass through unchanged")
	}
}

func TestRenewAction_ToleratesNoProgressCallback(t *testing.T) {
	a := &CertificateRenewAction{client: testClient(t, newFakeHarbour(chain("a")...))}
	resp := &action.InvokeResponse{} // SendProgress nil
	a.Invoke(context.Background(), renewRequest(t, a, "a"), resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
}

func TestRenewAction_ProviderRegistersItUnderTheExpectedName(t *testing.T) {
	p := &HarbourProvider{}
	actions := p.Actions(context.Background())
	if len(actions) != 1 {
		t.Fatalf("expected exactly one action, got %d", len(actions))
	}
	var mr action.MetadataResponse
	actions[0]().Metadata(context.Background(), action.MetadataRequest{ProviderTypeName: "harbour"}, &mr)
	if mr.TypeName != "harbour_certificate_renew" {
		t.Fatalf("type name = %q", mr.TypeName)
	}
}
