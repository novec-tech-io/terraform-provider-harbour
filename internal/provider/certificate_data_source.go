package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &CertificateDataSource{}

type CertificateDataSource struct {
	client *Client
}

type CertificateDataSourceModel struct {
	ID                types.String `tfsdk:"id"`
	RequestID         types.String `tfsdk:"request_id"`
	CommonName        types.String `tfsdk:"common_name"`
	TTL               types.String `tfsdk:"ttl"`
	SerialNumber      types.String `tfsdk:"serial_number"`
	SecretARN         types.String `tfsdk:"secret_arn"`
	ExpiryTimestamp   types.Int64  `tfsdk:"expiry_timestamp"`
	Status            types.String `tfsdk:"status"`
	ACMCertificateARN types.String `tfsdk:"acm_certificate_arn"`
}

func NewCertificateDataSource() datasource.DataSource {
	return &CertificateDataSource{}
}

func (d *CertificateDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_certificate"
}

func (d *CertificateDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing certificate from Harbour by request ID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
			},
			"request_id": schema.StringAttribute{
				Required:    true,
				Description: "The request ID of the certificate to read.",
			},
			"common_name": schema.StringAttribute{
				Computed:    true,
				Description: "Certificate common name (CN).",
			},
			"ttl": schema.StringAttribute{
				Computed:    true,
				Description: "Certificate TTL as issued (e.g. 90d).",
			},
			"serial_number": schema.StringAttribute{
				Computed: true,
			},
			"secret_arn": schema.StringAttribute{
				Computed:    true,
				Description: "Secrets Manager ARN where the certificate material is stored.",
			},
			"expiry_timestamp": schema.Int64Attribute{
				Computed:    true,
				Description: "Certificate expiry as a Unix timestamp.",
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Current certificate status (requested, issuing, issued, revoked, expired, failed).",
			},
			"acm_certificate_arn": schema.StringAttribute{
				Computed:    true,
				Description: "ARN of the certificate imported into ACM in the customer account. Null unless the certificate was issued with import_to_acm.",
			},
		},
	}
}

func (d *CertificateDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
	d.client = client
}

func (d *CertificateDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data CertificateDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	record, err := d.client.GetCertificate(ctx, data.RequestID.ValueString())
	if err != nil {
		var nfe *NotFoundError
		if errors.As(err, &nfe) {
			resp.Diagnostics.AddError("Certificate not found", err.Error())
			return
		}
		resp.Diagnostics.AddError("Failed to read certificate", err.Error())
		return
	}

	data.ID = types.StringValue(record.RequestID)
	data.RequestID = types.StringValue(record.RequestID)
	data.CommonName = types.StringValue(record.CN)
	data.TTL = types.StringValue(record.TTL)
	data.SerialNumber = types.StringValue(record.SerialNumber)
	data.SecretARN = types.StringValue(record.SecretARN)
	data.ExpiryTimestamp = types.Int64Value(record.ExpiryTimestamp)
	data.Status = types.StringValue(record.Status)
	if record.ACMCertificateARN != "" {
		data.ACMCertificateARN = types.StringValue(record.ACMCertificateARN)
	} else {
		data.ACMCertificateARN = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
