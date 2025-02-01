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
	_ datasource.DataSource              = &TablesDataSource{}
	_ datasource.DataSourceWithConfigure = &TablesDataSource{}
)

func NewTablesDataSource() datasource.DataSource {
	return &TablesDataSource{}
}

// TablesDataSource defines the data source implementation.
type TablesDataSource struct {
	conf *MySQLConfiguration
}

// TablesDataSourceModel describes the data source data model.
type TablesDataSourceModel struct {
	Database types.String `tfsdk:"database"`
	Pattern  types.String `tfsdk:"pattern"`
	Endpoint types.String `tfsdk:"endpoint"`
	// Username types.String `tfsdk:"username"`
	// Password types.String `tfsdk:"password"`
	Tables []TableModel `tfsdk:"tables"`
}

type TableModel struct {
	Table          types.String `tfsdk:"table"`
	TableType      types.String `tfsdk:"table_type"`
	TableCollation types.String `tfsdk:"table_collation"`
	TableRows      types.Int64  `tfsdk:"table_rows"`
	DataLength     types.Int64  `tfsdk:"data_length_bytes"`
	TableComment   types.String `tfsdk:"table_comment"`
}

func (d *TablesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tables"
}

func (d *TablesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// This description is used by the documentation generator and the language server.
		MarkdownDescription: "tables data source",

		Attributes: map[string]schema.Attribute{
			"database": schema.StringAttribute{
				MarkdownDescription: "database",
				Required:            true,
			},
			"pattern": schema.StringAttribute{
				MarkdownDescription: "tables pattern",
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
			"tables": schema.ListNestedAttribute{
				Description: "List of tables.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"table": schema.StringAttribute{
							MarkdownDescription: "table name.",
							Computed:            true,
						},
						"table_type": schema.StringAttribute{
							MarkdownDescription: "table_type for the table.",
							Computed:            true,
						},
						"table_collation": schema.StringAttribute{
							MarkdownDescription: "table_collation for the table.",
							Computed:            true,
						},
						"data_length_bytes": schema.Int64Attribute{
							MarkdownDescription: "data_length_bytes for the table.",
							Computed:            true,
						},
						"table_rows": schema.Int64Attribute{
							MarkdownDescription: "table_rows for the table.",
							Computed:            true,
						},
						"table_comment": schema.StringAttribute{
							MarkdownDescription: "table_comment for the table.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *TablesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
	tflog.Debug(ctx, fmt.Sprintf("tables_data_source Configure conf: %+v", *conf))

	d.conf = conf
}

func (d *TablesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data TablesDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("TablesDataSourceModel: %+v", data))

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

	sql := fmt.Sprintf(
		"SELECT TABLE_NAME,TABLE_TYPE,TABLE_COLLATION,TABLE_ROWS,DATA_LENGTH,TABLE_COMMENT"+
			" FROM information_schema.TABLES"+
			" WHERE TABLE_SCHEMA='%s'",
		data.Database.ValueString(),
	)
	if !data.Pattern.IsNull() {
		sql += fmt.Sprintf(" AND TABLE_NAME LIKE '%s'", data.Pattern.ValueString())
	}

	db, err := getDatabaseFromMeta(ctx, &conf)
	if err != nil {
		resp.Diagnostics.AddError("Create MySQL Conn err", err.Error())
		return
	}
	tflog.Debug(ctx, fmt.Sprintf("SQL: %s", sql))

	rows, err := db.QueryContext(ctx, sql)
	if err != nil {
		resp.Diagnostics.AddError("failed querying for tables", err.Error())
		return
	}
	defer rows.Close()

	for rows.Next() {
		var table_name, table_type, table_collation, table_comment string
		var table_rows, data_length int64

		if err := rows.Scan(&table_name, &table_type, &table_collation, &table_rows, &data_length, &table_comment); err != nil {
			resp.Diagnostics.AddError("failed scanning MySQL rows", err.Error())
			return
		}
		table := TableModel{
			Table:          types.StringValue(table_name),
			TableType:      types.StringValue(table_type),
			TableCollation: types.StringValue(table_collation),
			TableRows:      types.Int64Value(table_rows),
			DataLength:     types.Int64Value(data_length),
			TableComment:   types.StringValue(table_comment),
		}

		data.Tables = append(data.Tables, table)
	}

	// Write logs using the tflog package
	// Documentation: https://terraform.io/plugin/log
	tflog.Trace(ctx, "read a data source")

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
