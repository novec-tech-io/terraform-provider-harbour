package provider

// Regression tests for KAN-171: Harbour renewals are write-once (a new
// request_id per renewal, the old record goes `superseded`), so a Terraform
// resource that keeps the request_id it was created with ends up bound to a
// dead record. These run the real Client (real SigV4 signing, injected static
// credentials) against an httptest server standing in for the Harbour API,
// and drive the resources' real Read/Delete through the plugin framework's
// request/response types.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// fakeHarbour is the slice of the Harbour API these resources touch:
// GET /certificates/{id} and POST /certificates/revoke, with the real API's
// 404 (unknown) and 409 (already revoked/superseded) behaviour.
type fakeHarbour struct {
	mu      sync.Mutex
	records map[string]*CertificateRecord
	revoked []string // request_ids a revoke call actually succeeded on
	gets    []string

	renewCalls  []string // request_ids POST .../renew was called with
	renewStatus int      // if non-zero, every renew call fails with this HTTP status
}

func newFakeHarbour(records ...*CertificateRecord) *fakeHarbour {
	f := &fakeHarbour{records: map[string]*CertificateRecord{}}
	for _, r := range records {
		f.records[r.RequestID] = r
	}
	return f
}

func (f *fakeHarbour) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/certificates/"):
		id := strings.TrimPrefix(r.URL.Path, "/certificates/")
		f.gets = append(f.gets, id)
		rec, ok := f.records[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":["request not found"]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(rec)
	case r.Method == http.MethodPost && r.URL.Path == "/certificates/revoke":
		var body RevokeRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec, ok := f.records[body.RequestID]
		switch {
		case !ok:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":["request not found"]}`))
		case rec.Status == "revoked" || rec.Status == "superseded":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"errors":["certificate is already ` + rec.Status + `"]}`))
		default:
			rec.Status = "revoked"
			f.revoked = append(f.revoked, body.RequestID)
			_, _ = w.Write([]byte(`{"status":"revoked"}`))
		}
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/certificates/") &&
		strings.HasSuffix(r.URL.Path, "/renew"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/certificates/"), "/renew")
		f.renewCalls = append(f.renewCalls, id)
		if f.renewStatus != 0 {
			w.WriteHeader(f.renewStatus)
			_, _ = w.Write([]byte(`{"errors":["forced failure"]}`))
			return
		}
		rec, ok := f.records[id]
		switch {
		case !ok:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":["request not found"]}`))
		case rec.Status != "issued":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"errors":["certificate is ` + rec.Status + `; only an issued certificate can be renewed"]}`))
		default:
			next := *rec
			next.RequestID = id + "-r"
			next.SerialNumber = "serial-" + next.RequestID
			next.Status = "issued"
			next.RenewedToRequestID = ""
			rec.Status = "superseded"
			rec.RenewedToRequestID = next.RequestID
			f.records[next.RequestID] = &next
			_ = json.NewEncoder(w).Encode(RenewCertResponse{OldRequestID: id, NewRequestID: next.RequestID, Certificate: next})
		}
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func testClient(t *testing.T, f *fakeHarbour) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	creds := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "AKIATEST", SecretAccessKey: "secret"}, nil
	})
	return NewClient(srv.URL, "eu-west-1", aws.Config{Region: "eu-west-1", Credentials: creds})
}

// chain builds A(superseded)->B(superseded)->...->last(issued) with distinct
// serials/ARNs-per-record so a test can tell which record state came from.
func chain(ids ...string) []*CertificateRecord {
	out := make([]*CertificateRecord, len(ids))
	for i, id := range ids {
		rec := &CertificateRecord{
			RequestID:         id,
			CN:                "svc.example.test",
			Status:            "issued",
			SerialNumber:      "serial-" + id,
			SecretARN:         "arn:secret:" + id,
			ExpiryTimestamp:   int64(1000 + i),
			ACMCertificateARN: "arn:acm:stable",
		}
		if i < len(ids)-1 {
			rec.Status = "superseded"
			rec.RenewedToRequestID = ids[i+1]
		}
		out[i] = rec
	}
	return out
}

// ---- client: GetCurrentCertificate ----------------------------------------

func TestGetCurrentCertificate_NoRenewalReturnsSameRecord(t *testing.T) {
	f := newFakeHarbour(chain("a")...)
	got, err := testClient(t, f).GetCurrentCertificate(context.Background(), "a")
	if err != nil || got.RequestID != "a" {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestGetCurrentCertificate_FollowsOneHop(t *testing.T) {
	f := newFakeHarbour(chain("a", "b")...)
	got, err := testClient(t, f).GetCurrentCertificate(context.Background(), "a")
	if err != nil || got.RequestID != "b" || got.Status != "issued" {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestGetCurrentCertificate_FollowsManyHops(t *testing.T) {
	f := newFakeHarbour(chain("a", "b", "c", "d", "e")...)
	got, err := testClient(t, f).GetCurrentCertificate(context.Background(), "a")
	if err != nil || got.RequestID != "e" {
		t.Fatalf("got %+v, err %v", got, err)
	}
	if len(f.gets) != 5 {
		t.Fatalf("expected one GET per record in the chain, got %v", f.gets)
	}
}

func TestGetCurrentCertificate_StartingMidChainStillResolvesToTheEnd(t *testing.T) {
	f := newFakeHarbour(chain("a", "b", "c")...)
	got, err := testClient(t, f).GetCurrentCertificate(context.Background(), "b")
	if err != nil || got.RequestID != "c" {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestGetCurrentCertificate_ChainEndingRevokedIsReportedAsRevoked(t *testing.T) {
	recs := chain("a", "b")
	recs[1].Status = "revoked"
	got, err := testClient(t, newFakeHarbour(recs...)).GetCurrentCertificate(context.Background(), "a")
	if err != nil || got.RequestID != "b" || got.Status != "revoked" {
		t.Fatalf("a chain ending revoked must say so, got %+v, err %v", got, err)
	}
}

func TestGetCurrentCertificate_SupersededWithoutPointerIsReturnedAsIs(t *testing.T) {
	rec := &CertificateRecord{RequestID: "a", Status: "superseded"}
	got, err := testClient(t, newFakeHarbour(rec)).GetCurrentCertificate(context.Background(), "a")
	if err != nil || got.RequestID != "a" {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestGetCurrentCertificate_UnknownStartIsNotFound(t *testing.T) {
	_, err := testClient(t, newFakeHarbour()).GetCurrentCertificate(context.Background(), "nope")
	if _, ok := err.(*NotFoundError); !ok {
		t.Fatalf("expected *NotFoundError, got %T %v", err, err)
	}
}

func TestGetCurrentCertificate_MissingSuccessorIsAHardErrorNotNotFound(t *testing.T) {
	// The original exists but points at a successor that doesn't. If this were
	// a NotFoundError, Read would drop the resource from state silently.
	rec := &CertificateRecord{RequestID: "a", Status: "superseded", RenewedToRequestID: "ghost"}
	_, err := testClient(t, newFakeHarbour(rec)).GetCurrentCertificate(context.Background(), "a")
	if err == nil {
		t.Fatal("expected an error")
	}
	if _, ok := err.(*NotFoundError); ok {
		t.Fatalf("a missing successor must not be a NotFoundError: %v", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error should name the missing successor: %v", err)
	}
}

func TestGetCurrentCertificate_LoopIsAnError(t *testing.T) {
	a := &CertificateRecord{RequestID: "a", Status: "superseded", RenewedToRequestID: "b"}
	b := &CertificateRecord{RequestID: "b", Status: "superseded", RenewedToRequestID: "a"}
	_, err := testClient(t, newFakeHarbour(a, b)).GetCurrentCertificate(context.Background(), "a")
	if err == nil || !strings.Contains(err.Error(), "loops") {
		t.Fatalf("expected a loop error, got %v", err)
	}
}

func TestGetCurrentCertificate_OverlongChainIsAnError(t *testing.T) {
	ids := make([]string, maxRenewalChainHops+5)
	for i := range ids {
		ids[i] = "r" + string(rune('A'+i/26)) + string(rune('a'+i%26))
	}
	_, err := testClient(t, newFakeHarbour(chain(ids...)...)).GetCurrentCertificate(context.Background(), ids[0])
	if err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Fatalf("expected a hop-cap error, got %v", err)
	}
}

// ---- resource: harbour_certificate Read/Delete after a renewal -------------

func certState(t *testing.T, r *CertificateResource, requestID string) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
	model := CertificateResourceModel{
		ID:                types.StringValue(requestID),
		RequestID:         types.StringValue(requestID),
		CommonName:        types.StringValue("svc.example.test"),
		TTL:               types.StringValue("90d"),
		AltNames:          types.ListNull(types.StringType),
		ExportToACM:       types.BoolValue(true),
		CSR:               types.StringNull(),
		DeliveryAccountID: types.StringNull(),
		AllowDuplicateCN:  types.BoolNull(),
		SerialNumber:      types.StringValue("serial-" + requestID),
		SecretARN:         types.StringValue("arn:secret:" + requestID),
		ExpiryTimestamp:   types.Int64Value(1000),
		Status:            types.StringValue("issued"),
		ACMCertificateARN: types.StringValue("arn:acm:stable"),
	}
	if diags := st.Set(ctx, &model); diags.HasError() {
		t.Fatalf("building state: %v", diags)
	}
	return st
}

func TestCertificateResource_ReadAfterRenewalMovesStateToTheSuccessor(t *testing.T) {
	ctx := context.Background()
	f := newFakeHarbour(chain("old", "new")...)
	r := &CertificateResource{client: testClient(t, f)}
	st := certState(t, r, "old")

	resp := resource.ReadResponse{State: st}
	r.Read(ctx, resource.ReadRequest{State: st}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Read errored: %v", resp.Diagnostics)
	}

	var rid, id, serial, secret, status string
	resp.State.GetAttribute(ctx, path.Root("request_id"), &rid)
	resp.State.GetAttribute(ctx, path.Root("id"), &id)
	resp.State.GetAttribute(ctx, path.Root("serial_number"), &serial)
	resp.State.GetAttribute(ctx, path.Root("secret_arn"), &secret)
	resp.State.GetAttribute(ctx, path.Root("status"), &status)
	if rid != "new" || id != "new" || serial != "serial-new" || secret != "arn:secret:new" || status != "issued" {
		t.Fatalf("state not moved to the successor: request_id=%s id=%s serial=%s secret=%s status=%s",
			rid, id, serial, secret, status)
	}
}

func TestCertificateResource_ReadWithoutRenewalIsUnchanged(t *testing.T) {
	ctx := context.Background()
	f := newFakeHarbour(chain("only")...)
	r := &CertificateResource{client: testClient(t, f)}
	st := certState(t, r, "only")
	resp := resource.ReadResponse{State: st}
	r.Read(ctx, resource.ReadRequest{State: st}, &resp)
	var rid string
	resp.State.GetAttribute(ctx, path.Root("request_id"), &rid)
	if resp.Diagnostics.HasError() || rid != "only" {
		t.Fatalf("rid=%s diags=%v", rid, resp.Diagnostics)
	}
}

func TestCertificateResource_ReadOfDeletedCertificateRemovesItFromState(t *testing.T) {
	ctx := context.Background()
	r := &CertificateResource{client: testClient(t, newFakeHarbour())}
	st := certState(t, r, "gone")
	resp := resource.ReadResponse{State: st}
	r.Read(ctx, resource.ReadRequest{State: st}, &resp)
	if !resp.State.Raw.IsNull() {
		t.Fatal("a genuinely missing certificate must be removed from state")
	}
}

// The exact leak found live on Meridian: destroy after a renewal used to
// revoke the superseded id (a swallowed 409) and leave the successor issued.
func TestCertificateResource_DeleteAfterRenewalRevokesTheLiveSuccessor(t *testing.T) {
	ctx := context.Background()
	f := newFakeHarbour(chain("old", "new")...)
	r := &CertificateResource{client: testClient(t, f)}
	st := certState(t, r, "old") // state still holds the ORIGINAL request_id

	var resp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: st}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete errored: %v", resp.Diagnostics)
	}
	if len(f.revoked) != 1 || f.revoked[0] != "new" {
		t.Fatalf("expected exactly the live successor to be revoked, got %v", f.revoked)
	}
	if f.records["new"].Status != "revoked" {
		t.Fatalf("successor still %s", f.records["new"].Status)
	}
}

func TestCertificateResource_DeleteAcrossSeveralRenewals(t *testing.T) {
	ctx := context.Background()
	f := newFakeHarbour(chain("v1", "v2", "v3", "v4")...)
	r := &CertificateResource{client: testClient(t, f)}
	var resp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: certState(t, r, "v1")}, &resp)
	if resp.Diagnostics.HasError() || len(f.revoked) != 1 || f.revoked[0] != "v4" {
		t.Fatalf("revoked=%v diags=%v", f.revoked, resp.Diagnostics)
	}
}

func TestCertificateResource_DeleteWithoutRenewalStillRevokes(t *testing.T) {
	ctx := context.Background()
	f := newFakeHarbour(chain("only")...)
	r := &CertificateResource{client: testClient(t, f)}
	var resp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: certState(t, r, "only")}, &resp)
	if resp.Diagnostics.HasError() || len(f.revoked) != 1 || f.revoked[0] != "only" {
		t.Fatalf("revoked=%v diags=%v", f.revoked, resp.Diagnostics)
	}
}

func TestCertificateResource_DeleteOfAlreadyRevokedCertificateSucceeds(t *testing.T) {
	ctx := context.Background()
	recs := chain("old", "new")
	recs[1].Status = "revoked" // e.g. someone revoked the live cert out of band
	f := newFakeHarbour(recs...)
	r := &CertificateResource{client: testClient(t, f)}
	var resp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: certState(t, r, "old")}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("an already-revoked certificate must not fail destroy: %v", resp.Diagnostics)
	}
}

func TestCertificateResource_DeleteOfMissingCertificateSucceeds(t *testing.T) {
	ctx := context.Background()
	r := &CertificateResource{client: testClient(t, newFakeHarbour())}
	var resp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: certState(t, r, "gone")}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a missing certificate must not fail destroy: %v", resp.Diagnostics)
	}
}

func TestCertificateResource_DeleteWithBrokenChainFailsLoudlyAndRevokesNothing(t *testing.T) {
	ctx := context.Background()
	rec := &CertificateRecord{RequestID: "old", Status: "superseded", RenewedToRequestID: "ghost"}
	f := newFakeHarbour(rec)
	r := &CertificateResource{client: testClient(t, f)}
	var resp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: certState(t, r, "old")}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("destroy must fail rather than silently succeed with the live cert unaccounted for")
	}
	if len(f.revoked) != 0 {
		t.Fatalf("nothing should have been revoked, got %v", f.revoked)
	}
}
