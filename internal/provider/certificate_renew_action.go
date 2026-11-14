package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ action.Action              = &CertificateRenewAction{}
	_ action.ActionWithConfigure = &CertificateRenewAction{}
)

// CertificateRenewAction renews one certificate right now, ahead of its
// renewal window (KAN-166's POST /certificates/{request_id}/renew).
//
// It is an action, not a resource attribute, because "renew now" is an
// imperative one-off: it has no desired state to converge on. Actions see
// only their own config (no state, no outputs), so after it runs the
// harbour_certificate in state is behind the live certificate -- the next
// refresh follows the renewal chain (KAN-171) and shows the new request_id,
// serial and expiry as ordinary drift. Needs Terraform 1.14+.
type CertificateRenewAction struct {
	client *Client
}

type CertificateRenewActionModel struct {
	RequestID types.String `tfsdk:"request_id"`
}

func NewCertificateRenewAction() action.Action {
	return &CertificateRenewAction{}
}

func (a *CertificateRenewAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_certificate_renew"
}

func (a *CertificateRenewAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Renews a Harbour certificate immediately, ahead of its automatic renewal window. " +
			"Run it with `terraform apply -invoke=action.harbour_certificate_renew.<name>`, or attach it to a " +
			"resource's `lifecycle { action_trigger { ... } }`.",
		Attributes: map[string]schema.Attribute{
			"request_id": schema.StringAttribute{
				Required: true,
				Description: "request_id of the certificate to renew (typically `harbour_certificate.<name>.request_id`). " +
					"Any request_id in the certificate's renewal history works: the action resolves it to the current live certificate first.",
			},
		},
	}
}

func (a *CertificateRenewAction) Configure(_ context.Context, req action.ConfigureRequest, resp *action.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected *Client, got %T", req.ProviderData),
		)
		return
	}
	a.client = client
}

func (a *CertificateRenewAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var data CertificateRenewActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	progress := func(msg string) {
		if resp.SendProgress != nil {
			resp.SendProgress(action.InvokeProgressEvent{Message: msg})
		}
	}

	// Resolve to the live record first: the request_id in config may come
	// from state that is behind a renewal (KAN-171), and only the live
	// record can be renewed.
	current, err := a.client.GetCurrentCertificate(ctx, data.RequestID.ValueString())
	if err != nil {
		var nfe *NotFoundError
		if errors.As(err, &nfe) {
			resp.Diagnostics.AddError("Certificate not found",
				fmt.Sprintf("No certificate with request_id %q exists in this Harbour deployment.", data.RequestID.ValueString()))
			return
		}
		resp.Diagnostics.AddError("Failed to look up the certificate", err.Error())
		return
	}

	progress(fmt.Sprintf("Renewing %s (request_id %s)...", current.CN, current.RequestID))

	renewed, err := a.client.RenewCertificate(ctx, current.RequestID)
	if err != nil {
		var ce *ConflictError
		if errors.As(err, &ce) {
			resp.Diagnostics.AddError("Certificate cannot be renewed", ce.Message)
			return
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			switch apiErr.Status {
			case http.StatusTooManyRequests:
				resp.Diagnostics.AddError("Renewal not allowed right now", apiErr.Messages())
			case http.StatusBadGateway:
				resp.Diagnostics.AddError("Renewal failed",
					apiErr.Messages()+"\n\nThe existing certificate is unchanged. Run a plan to check its current state before retrying.")
			default:
				resp.Diagnostics.AddError("Failed to renew the certificate", apiErr.Messages())
			}
			return
		}
		resp.Diagnostics.AddError("Failed to renew the certificate", err.Error())
		return
	}

	msg := fmt.Sprintf("Renewed %s: request_id %s -> %s", current.CN, current.RequestID, renewed.NewRequestID)
	if renewed.Certificate.ACMCertificateARN != "" {
		msg += fmt.Sprintf(" (ACM ARN unchanged: %s)", renewed.Certificate.ACMCertificateARN)
	}
	progress(msg)
	progress("The harbour_certificate in state is now behind: run `terraform apply -refresh-only` (or any plan/apply) to pick up the new request_id.")
}
