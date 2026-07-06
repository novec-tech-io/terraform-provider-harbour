package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &CertificateImportResource{}

type CertificateImportResource struct {
	client *Client
}

type CertificateImportResourceModel struct {
	ID                types.String `tfsdk:"id"`
	ACMCertificateARN types.String `tfsdk:"acm_certificate_arn"`
	AutoRenew         types.Bool   `tfsdk:"auto_renew"`
	RequestID         types.String `tfsdk:"request_id"`
	CN                types.String `tfsdk:"cn"`
	SANs              types.List   `tfsdk:"sans"`
	SerialNumber      types.String `tfsdk:"serial_number"`
	SecretARN         types.String `tfsdk:"secret_arn"`
	ExpiryTimestamp   types.Int64  `tfsdk:"expiry_timestamp"`
	Status            types.String `tfsdk:"status"`
	IssuanceMethod    types.String `tfsdk:"issuance_method"`
}

func NewCertificateImportResource() resource.Resource {
	return &CertificateImportResource{}
}

func (r *CertificateImportResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_certificate_import"
}

func (r *CertificateImportResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Registers a certificate that already lives in ACM (imported by some other means) for Harbour lifecycle tracking — renewal, alerting, and revocation — without Harbour ever holding or needing the private key. The ARN's ACM Type must be IMPORTED; AMAZON_ISSUED certificates can't be adopted this way. No cert material changes at registration time — the existing ACM object is left untouched until the ordinary renewal-window scan reissues it through Harbour's CA, same as any harbour_certificate with export_to_acm. Destroying this resource revokes the registration; until the first renewal happens, that can only delete the ACM object (acm_only), since Harbour never signed this certificate.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"acm_certificate_arn": schema.StringAttribute{
				Required:    true,
				Description: "ARN of an existing ACM certificate in your account (Type must be IMPORTED) to register with Harbour.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"auto_renew": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Whether Harbour proactively renews this certificate ahead of expiry. Defaults to the tenant's default_auto_renew config value when omitted.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"request_id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cn": schema.StringAttribute{
				Computed:    true,
				Description: "Common name, read from the ACM certificate's DomainName.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"sans": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Subject alternative names, read from the ACM certificate.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
			},
			"serial_number": schema.StringAttribute{
				Computed:    true,
				Description: "Null until this certificate's first Harbour-managed renewal — Harbour never signed the originally-imported material, so there's no CA-side serial for it.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"secret_arn": schema.StringAttribute{
				Computed:    true,
				Description: "Null until this certificate's first Harbour-managed renewal, same reasoning as serial_number.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"expiry_timestamp": schema.Int64Attribute{
				Computed:    true,
				Description: "Certificate expiry as a Unix timestamp, read from the ACM certificate at registration time.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Current certificate status (issued, renewing, revoked, expired).",
			},
			"issuance_method": schema.StringAttribute{
				Computed:    true,
				Description: "Always \"imported\" for this resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *CertificateImportResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
	r.client = client
}

func (r *CertificateImportResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data CertificateImportResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	importReq := ImportCertRequest{
		ACMCertificateARN: data.ACMCertificateARN.ValueString(),
	}
	if !data.AutoRenew.IsNull() && !data.AutoRenew.IsUnknown() {
		v := data.AutoRenew.ValueBool()
		importReq.AutoRenew = &v
	}

	record, err := r.client.ImportCertificate(ctx, importReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to import certificate", err.Error())
		return
	}

	resp.Diagnostics.Append(r.recordToModel(ctx, record, &data)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *CertificateImportResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data CertificateImportResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	record, err := r.client.GetCertificate(ctx, data.RequestID.ValueString())
	if err != nil {
		var nfe *NotFoundError
		if errors.As(err, &nfe) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read certificate", err.Error())
		return
	}

	resp.Diagnostics.Append(r.recordToModel(ctx, record, &data)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *CertificateImportResource) Update(_ context.Context, _ resource.UpdateRequest, _ *resource.UpdateResponse) {
	// All arguments carry RequiresReplace — Update is never reached.
}

func (r *CertificateImportResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data CertificateImportResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.RevokeCertificate(ctx, RevokeRequest{RequestID: data.RequestID.ValueString()})
	if err != nil {
		var nfe *NotFoundError
		var ce *ConflictError
		if errors.As(err, &nfe) || errors.As(err, &ce) {
			return
		}
		resp.Diagnostics.AddError("Failed to revoke certificate", err.Error())
	}
}

func (r *CertificateImportResource) recordToModel(ctx context.Context, record *CertificateRecord, data *CertificateImportResourceModel) (diags diag.Diagnostics) {
	data.ID = types.StringValue(record.RequestID)
	data.RequestID = types.StringValue(record.RequestID)
	data.CN = types.StringValue(record.CN)
	data.ExpiryTimestamp = types.Int64Value(record.ExpiryTimestamp)
	data.Status = types.StringValue(record.Status)
	data.IssuanceMethod = types.StringValue(record.IssuanceMethod)
	data.AutoRenew = types.BoolValue(record.AutoRenew == "true")

	if record.SerialNumber != "" {
		data.SerialNumber = types.StringValue(record.SerialNumber)
	} else {
		data.SerialNumber = types.StringNull()
	}
	if record.SecretARN != "" {
		data.SecretARN = types.StringValue(record.SecretARN)
	} else {
		data.SecretARN = types.StringNull()
	}

	sansList, d := types.ListValueFrom(ctx, types.StringType, record.SANs)
	diags.Append(d...)
	data.SANs = sansList

	return diags
}
