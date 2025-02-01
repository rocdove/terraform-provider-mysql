package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/jinzhu/copier"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ datasource.DataSource              = &DatabasesDataSource{}
	_ datasource.DataSourceWithConfigure = &DatabasesDataSource{}
)

func NewDatabasesDataSource() datasource.DataSource {
	return &DatabasesDataSource{}
}

// DatabasesDataSource defines the data source implementation.
type DatabasesDataSource struct {
	conf *MySQLConfiguration
}

// DatabasesDataSourceModel describes the data source data model.
type DatabasesDataSourceModel struct {
	Pattern  types.String `tfsdk:"pattern"`
	Endpoint types.String `tfsdk:"endpoint"`
	// Username  types.String    `tfsdk:"username"`
	// Password  types.String    `tfsdk:"password"`
	Databases []DatabaseModel `tfsdk:"databases"`
}

type DatabaseModel struct {
	Database            types.String `tfsdk:"database"`
	DefaultCharacterSet types.String `tfsdk:"default_character_set"`
	DefaultCollation    types.String `tfsdk:"default_collation"`
}

func (d *DatabasesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_databases"
}

func (d *DatabasesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// This description is used by the documentation generator and the language server.
		MarkdownDescription: "databases data source",

		Attributes: map[string]schema.Attribute{
			"pattern": schema.StringAttribute{
				MarkdownDescription: "database pattern",
				Optional:            true,
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Endpoint for MySQL. May also be provided via MYSQL_ENDPOINT environment variable.",
				Optional:            true,
			},
			// "username": schema.StringAttribute{
			// 	MarkdownDescription: "Username for MySQL. May also be provided via MYSQL_USERNAME environment variable.",
			// 	Optional:            true,
			// },
			// "password": schema.StringAttribute{
			// 	MarkdownDescription: "Password for MySQL. May also be provided via MYSQL_PASSWORD environment variable.",
			// 	Optional:            true,
			// 	Sensitive:           true,
			// },
			"databases": schema.ListNestedAttribute{
				Description: "List of database.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"database": schema.StringAttribute{
							MarkdownDescription: "database name.",
							Computed:            true,
						},
						"default_character_set": schema.StringAttribute{
							MarkdownDescription: "default_character_set for the database.",
							Computed:            true,
						},
						"default_collation": schema.StringAttribute{
							MarkdownDescription: "default_collation for the database.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *DatabasesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}

	conf, ok := req.ProviderData.(*MySQLConfiguration)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *MySQLConfiguration, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)

		return
	}
	tflog.Debug(ctx, fmt.Sprintf("database_data_source Configure conf: %+v", *conf))

	d.conf = conf
}

func (d *DatabasesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data DatabasesDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("DatabasesDataSourceModel: %+v", data))

	var conf MySQLConfiguration
	err := copier.Copy(&conf, d.conf)
	if err != nil {
		resp.Diagnostics.AddError("Error copying struct:", err.Error())
		return
	}

	if !data.Endpoint.IsNull() {
		conf.Config.Addr = data.Endpoint.ValueString()
	}
	// if !data.Username.IsNull() {
	// 	conf.Config.User = data.Username.ValueString()
	// }
	// if !data.Password.IsNull() {
	// 	conf.Config.Passwd = data.Password.ValueString()
	// }

	sql := "SELECT SCHEMA_NAME,DEFAULT_CHARACTER_SET_NAME,DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA"
	if !data.Pattern.IsNull() {
		sql += fmt.Sprintf(" WHERE SCHEMA_NAME LIKE '%s'", data.Pattern.ValueString())
	}

	db, err := getDatabaseFromMeta(ctx, &conf)
	if err != nil {
		resp.Diagnostics.AddError("Create MySQL Conn err", err.Error())
		return
	}
	tflog.Debug(ctx, fmt.Sprintf("SQL: %s", sql))

	rows, err := db.QueryContext(ctx, sql)
	if err != nil {
		resp.Diagnostics.AddError("failed querying for databases", err.Error())
		return
	}
	defer rows.Close()

	for rows.Next() {
		var database_name, default_character_set, default_collation string

		if err := rows.Scan(&database_name, &default_character_set, &default_collation); err != nil {
			resp.Diagnostics.AddError("failed scanning MySQL rows", err.Error())
			return
		}
		database := DatabaseModel{
			Database:            types.StringValue(database_name),
			DefaultCharacterSet: types.StringValue(default_character_set),
			DefaultCollation:    types.StringValue(default_collation),
		}

		data.Databases = append(data.Databases, database)
	}

	// Write logs using the tflog package
	// Documentation: https://terraform.io/plugin/log
	tflog.Trace(ctx, "read a data source")

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
