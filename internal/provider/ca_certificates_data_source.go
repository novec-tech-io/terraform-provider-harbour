package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &CACertificatesDataSource{}

type CACertificatesDataSource struct {
	client *Client
}

type CACertificatesDataSourceModel struct {
	Roots           types.List   `tfsdk:"roots"`
	Intermediates   types.List   `tfsdk:"intermediates"`
	RootPEM         types.String `tfsdk:"root_pem"`
	IntermediatePEM types.String `tfsdk:"intermediate_pem"`
	ChainPEM        types.String `tfsdk:"chain_pem"`
}

func NewCACertificatesDataSource() datasource.DataSource {
	return &CACertificatesDataSource{}
}

func (d *CACertificatesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ca_certificates"
}

func (d *CACertificatesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads Harbour's CA certificates for trust-store distribution. Anchor trust on the root certificate. The list attributes contain a single element today; during a CA rotation or a bring-your-own-root migration window they may contain two overlapping anchors.",
		Attributes: map[string]schema.Attribute{
			"roots": schema.ListAttribute{
				ElementType: types.StringType,
				Computed:    true,
				Description: "PEM-encoded root CA certificates. Install these in trust stores.",
			},
			"intermediates": schema.ListAttribute{
				ElementType: types.StringType,
				Computed:    true,
				Description: "PEM-encoded intermediate CA certificates.",
			},
			"root_pem": schema.StringAttribute{
				Computed:    true,
				Description: "The first root CA certificate, as PEM. Convenience scalar for the common single-root case.",
			},
			"intermediate_pem": schema.StringAttribute{
				Computed:    true,
				Description: "The first intermediate CA certificate, as PEM. Convenience scalar for the common single-intermediate case.",
			},
			"chain_pem": schema.StringAttribute{
				Computed:    true,
				Description: "The full CA chain as concatenated PEM, intermediate first, root last.",
			},
		},
	}
}

func (d *CACertificatesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *CACertificatesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data CACertificatesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ca, err := d.client.GetCA(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read CA certificates", err.Error())
		return
	}

	roots, diags := types.ListValueFrom(ctx, types.StringType, ca.Roots)
	resp.Diagnostics.Append(diags...)
	intermediates, diags := types.ListValueFrom(ctx, types.StringType, ca.Intermediates)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.Roots = roots
	data.Intermediates = intermediates
	if len(ca.Roots) > 0 {
		data.RootPEM = types.StringValue(ca.Roots[0])
	} else {
		data.RootPEM = types.StringNull()
	}
	if len(ca.Intermediates) > 0 {
		data.IntermediatePEM = types.StringValue(ca.Intermediates[0])
	} else {
		data.IntermediatePEM = types.StringNull()
	}
	data.ChainPEM = types.StringValue(ca.ChainPEM)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
