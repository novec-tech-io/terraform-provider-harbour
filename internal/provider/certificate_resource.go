package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &CertificateResource{}

type CertificateResource struct {
	client *Client
}

type CertificateResourceModel struct {
	ID                types.String `tfsdk:"id"`
	CommonName        types.String `tfsdk:"common_name"`
	TTL               types.String `tfsdk:"ttl"`
	AltNames          types.List   `tfsdk:"alt_names"`
	ImportToACM       types.Bool   `tfsdk:"import_to_acm"`
	RequestID         types.String `tfsdk:"request_id"`
	SerialNumber      types.String `tfsdk:"serial_number"`
	SecretARN         types.String `tfsdk:"secret_arn"`
	ExpiryTimestamp   types.Int64  `tfsdk:"expiry_timestamp"`
	Status            types.String `tfsdk:"status"`
	ACMCertificateARN types.String `tfsdk:"acm_certificate_arn"`
}

func NewCertificateResource() resource.Resource {
	return &CertificateResource{}
}

func (r *CertificateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_certificate"
}

func (r *CertificateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Issues and manages a certificate in a Harbour deployment. Destroying this resource revokes the certificate.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"common_name": schema.StringAttribute{
				Required:    true,
				Description: "Certificate common name (CN).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"ttl": schema.StringAttribute{
				Optional:    true,
				Description: "Certificate TTL (e.g. 90d, 8760h). Defaults to the tenant default_cert_ttl when omitted.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"alt_names": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Subject alternative names (SANs).",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.RequiresReplace(),
				},
			},
			"import_to_acm": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Import the issued certificate into ACM in the customer account via the tenant's configured cross-account role. Requires ACM import to be configured for this tenant (see harbour-acm-import IAM role).",
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
			"serial_number": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"secret_arn": schema.StringAttribute{
				Computed:    true,
				Description: "Secrets Manager ARN where the certificate material is stored.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"expiry_timestamp": schema.Int64Attribute{
				Computed:    true,
				Description: "Certificate expiry as a Unix timestamp.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Current certificate status (requested, issuing, issued, revoked, expired, failed).",
			},
			"acm_certificate_arn": schema.StringAttribute{
				Computed:    true,
				Description: "ARN of the certificate imported into ACM in the customer account. Only set when import_to_acm is true. Usable directly as certificate_arn on AWS resources such as aws_lb_listener.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *CertificateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *CertificateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data CertificateResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	issueReq := IssueCertRequest{
		CommonName:  data.CommonName.ValueString(),
		TTL:         data.TTL.ValueString(),
		ImportToACM: data.ImportToACM.ValueBool(),
	}

	if !data.AltNames.IsNull() && !data.AltNames.IsUnknown() {
		var altNames []string
		resp.Diagnostics.Append(data.AltNames.ElementsAs(ctx, &altNames, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		issueReq.AltNames = altNames
	}

	issueResp, err := r.client.IssueCertificate(ctx, issueReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to issue certificate", err.Error())
		return
	}

	record, err := r.client.PollCertificate(ctx, issueResp.RequestID)
	if err != nil {
		resp.Diagnostics.AddError("Certificate issuance failed", err.Error())
		return
	}

	r.recordToModel(record, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *CertificateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data CertificateResourceModel
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

	r.recordToModel(record, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *CertificateResource) Update(_ context.Context, _ resource.UpdateRequest, _ *resource.UpdateResponse) {
	// All arguments carry RequiresReplace — Update is never reached.
}

func (r *CertificateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data CertificateResourceModel
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

func (r *CertificateResource) recordToModel(record *CertificateRecord, data *CertificateResourceModel) {
	data.ID = types.StringValue(record.RequestID)
	data.RequestID = types.StringValue(record.RequestID)
	data.SerialNumber = types.StringValue(record.SerialNumber)
	data.SecretARN = types.StringValue(record.SecretARN)
	data.ExpiryTimestamp = types.Int64Value(record.ExpiryTimestamp)
	data.Status = types.StringValue(record.Status)
	if record.ACMCertificateARN != "" {
		data.ACMCertificateARN = types.StringValue(record.ACMCertificateARN)
	} else {
		data.ACMCertificateARN = types.StringNull()
	}
}
