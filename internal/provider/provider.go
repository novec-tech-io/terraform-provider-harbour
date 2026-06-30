package provider

import (
	"context"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &HarbourProvider{}

type HarbourProvider struct {
	version string
}

type HarbourProviderModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	Region   types.String `tfsdk:"region"`
	Profile  types.String `tfsdk:"profile"`
	RoleARN  types.String `tfsdk:"role_arn"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &HarbourProvider{version: version}
	}
}

func (p *HarbourProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "harbour"
	resp.Version = p.version
}

func (p *HarbourProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The Harbour provider issues and manages certificates in a Harbour private PKI deployment. It authenticates to the Harbour API using AWS SigV4 (execute-api).",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Required:    true,
				Description: "Harbour API endpoint URL.",
			},
			"region": schema.StringAttribute{
				Optional:    true,
				Description: "AWS region for SigV4 signing. Defaults to AWS_REGION / AWS_DEFAULT_REGION.",
			},
			"profile": schema.StringAttribute{
				Optional:    true,
				Description: "AWS profile name.",
			},
			"role_arn": schema.StringAttribute{
				Optional:    true,
				Description: "IAM role ARN to assume for API calls (e.g. the harbour-customer-{env} role).",
			},
		},
	}
}

func (p *HarbourProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data HarbourProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	region := data.Region.ValueString()
	if region == "" {
		region = os.Getenv("AWS_REGION")
	}
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		resp.Diagnostics.AddError("Missing AWS region", "Set region in the provider block or via AWS_REGION.")
		return
	}

	opts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
	}
	if profile := data.Profile.ValueString(); profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		resp.Diagnostics.AddError("Failed to load AWS config", err.Error())
		return
	}

	if roleARN := data.RoleARN.ValueString(); roleARN != "" {
		stsClient := sts.NewFromConfig(awsCfg)
		awsCfg.Credentials = aws.NewCredentialsCache(
			stscreds.NewAssumeRoleProvider(stsClient, roleARN),
		)
	}

	client := NewClient(data.Endpoint.ValueString(), region, awsCfg)
	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *HarbourProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewCertificateResource,
	}
}

func (p *HarbourProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewCertificateDataSource,
	}
}
