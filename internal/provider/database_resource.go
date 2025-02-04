// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/jinzhu/copier"
)

const (
	defaultCharacterSetKeyword = "CHARACTER SET "
	defaultCollateKeyword      = "COLLATE "
	unknownDatabaseErrCode     = 1049
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &DatabaseResource{}
	_ resource.ResourceWithConfigure   = &DatabaseResource{}
	_ resource.ResourceWithImportState = &DatabaseResource{}
)

func NewDatabaseResource() resource.Resource {
	return &DatabaseResource{}
}

// DatabaseResource defines the resource implementation.
type DatabaseResource struct {
	conf *MySQLConfiguration
}

// DatabaseResourceModel describes the resource data model.
type DatabaseResourceModel struct {
	ID                  types.String `tfsdk:"id"`
	Endpoint            types.String `tfsdk:"endpoint"`
	Database            types.String `tfsdk:"database"`
	DefaultCharacterSet types.String `tfsdk:"default_character_set"`
	DefaultCollation    types.String `tfsdk:"default_collation"`
}

func (r *DatabaseResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database"
}

func (r *DatabaseResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// This description is used by the documentation generator and the language server.
		MarkdownDescription: "Database resource",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "endpoint and database create to id(endpoint/database).",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Endpoint for MySQL. May also be provided via MYSQL_ENDPOINT environment variable.",
				Optional:            true,
				Computed:            true,
			},
			"database": schema.StringAttribute{
				MarkdownDescription: "database name.",
				Required:            true,
			},
			"default_character_set": schema.StringAttribute{
				MarkdownDescription: "default_character_set for the database.",
				Optional:            true,
				Computed:            true,
				// Default:             stringdefault.StaticString("utf8mb4"),
			},
			"default_collation": schema.StringAttribute{
				MarkdownDescription: "default_collation for the database.",
				Optional:            true,
				Computed:            true,
				// Default:             stringdefault.StaticString("utf8mb4_0900_ai_ci"),
			},
		},
	}
}

func (r *DatabaseResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}

	conf, ok := req.ProviderData.(*MySQLConfiguration)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *MySQLConfiguration, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)

		return
	}
	tflog.Debug(ctx, fmt.Sprintf("database_data_source Configure conf: %+v", *conf))

	r.conf = conf
}

func (r *DatabaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DatabaseResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Debug(ctx, fmt.Sprintf("DatabaseResourceModel: %+v", plan))

	var conf MySQLConfiguration
	err := copier.Copy(&conf, r.conf)
	if err != nil {
		resp.Diagnostics.AddError("Error copying struct:", err.Error())
		return
	}

	if !plan.Endpoint.IsNull() {
		conf.Config.Addr = plan.Endpoint.ValueString()
	}
	if conf.Config.Addr == "" {
		resp.Diagnostics.AddError("Error Endpoint is Null:", "MySQL Client Addr is Null!")
		return
	}

	db, err := getDatabaseFromMeta(ctx, &conf)
	if err != nil {
		resp.Diagnostics.AddError("Create MySQL Conn err", err.Error())
		return
	}
	stmtSQL := databaseConfigSQL(ctx, "CREATE", &plan)
	tflog.Debug(ctx, fmt.Sprintf("Executing statement: %s", stmtSQL))

	_, err = db.ExecContext(ctx, stmtSQL)
	if err != nil {
		resp.Diagnostics.AddError("failed running SQL to create DB", err.Error())
		return
	}
	plan.ID = types.StringValue(fmt.Sprintf("%s/%s", conf.Config.Addr, plan.Database.ValueString()))

	// Write logs using the tflog package
	// Documentation: https://terraform.io/plugin/log
	tflog.Trace(ctx, "created a resource")

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *DatabaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DatabaseResourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Debug(ctx, fmt.Sprintf("DatabaseResourceModel: %+v", state))

	id := state.ID.ValueString()
	if strings.Contains(id, "/") {
		parts := strings.Split(id, "/")
		state.Endpoint = types.StringValue(parts[0])
		state.Database = types.StringValue(parts[1])
	}

	var conf MySQLConfiguration
	err := copier.Copy(&conf, r.conf)
	if err != nil {
		resp.Diagnostics.AddError("Error copying struct:", err.Error())
		return
	}

	if !state.Endpoint.IsNull() {
		conf.Config.Addr = state.Endpoint.ValueString()
	}
	if conf.Config.Addr == "" {
		resp.Diagnostics.AddError("Error Endpoint is Null:", "MySQL Client Addr is Null!")
		return
	}

	db, err := getDatabaseFromMeta(ctx, &conf)
	if err != nil {
		resp.Diagnostics.AddError("Create MySQL Conn Error", err.Error())
		return
	}

	err = readDatabase(ctx, db, &state)
	if err != nil {
		resp.Diagnostics.AddError("Read Database Error", err.Error())
		return
	}

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DatabaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan DatabaseResourceModel

	// Read Terraform plan plan into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Debug(ctx, fmt.Sprintf("DatabaseResourceModel: %+v", plan))

	var conf MySQLConfiguration
	err := copier.Copy(&conf, r.conf)
	if err != nil {
		resp.Diagnostics.AddError("Error copying struct:", err.Error())
		return
	}

	if !plan.Endpoint.IsNull() {
		conf.Config.Addr = plan.Endpoint.ValueString()
	}
	if conf.Config.Addr == "" {
		resp.Diagnostics.AddError("Error Endpoint is Null:", "MySQL Client Addr is Null!")
		return
	}

	db, err := getDatabaseFromMeta(ctx, &conf)
	if err != nil {
		resp.Diagnostics.AddError("Create MySQL Conn err", err.Error())
		return
	}
	stmtSQL := databaseConfigSQL(ctx, "ALTER", &plan)
	tflog.Debug(ctx, fmt.Sprintf("Executing statement: %s", stmtSQL))

	_, err = db.ExecContext(ctx, stmtSQL)
	if err != nil {
		resp.Diagnostics.AddError("failed running SQL to alert DB", err.Error())
		return
	}
	err = readDatabase(ctx, db, &plan)
	if err != nil {
		resp.Diagnostics.AddError("Read Database Error", err.Error())
		return
	}

	plan.ID = types.StringValue(fmt.Sprintf("%s/%s", conf.Config.Addr, plan.Database.ValueString()))
	// Save updated plan into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *DatabaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DatabaseResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Debug(ctx, fmt.Sprintf("DatabaseResourceModel: %+v", state))

	var conf MySQLConfiguration
	err := copier.Copy(&conf, r.conf)
	if err != nil {
		resp.Diagnostics.AddError("Error copying struct:", err.Error())
		return
	}

	if !state.Endpoint.IsNull() {
		conf.Config.Addr = state.Endpoint.ValueString()
	}

	db, err := getDatabaseFromMeta(ctx, &conf)
	if err != nil {
		resp.Diagnostics.AddError("Create MySQL Conn Error", err.Error())
		return
	}

	stmtSQL := "DROP DATABASE " + quoteIdentifier(state.Database.ValueString())
	tflog.Debug(ctx, fmt.Sprintf("Executing statement: %s", stmtSQL))

	_, err = db.ExecContext(ctx, stmtSQL)
	if err != nil {
		resp.Diagnostics.AddError("failed running SQL to drop DB", err.Error())
		return
	}

}

func (r *DatabaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func databaseConfigSQL(ctx context.Context, verb string, d *DatabaseResourceModel) string {
	tflog.Trace(ctx, "config database stmt sql")
	name := d.Database.ValueString()
	defaultCharset := d.DefaultCharacterSet.ValueString()
	defaultCollation := d.DefaultCollation.ValueString()

	var defaultCharsetClause string
	var defaultCollationClause string

	if defaultCharset != "" {
		defaultCharsetClause = defaultCharacterSetKeyword + quoteIdentifier(defaultCharset)
	}
	if defaultCollation != "" {
		defaultCollationClause = defaultCollateKeyword + quoteIdentifier(defaultCollation)
	}

	return fmt.Sprintf(
		"%s DATABASE %s %s %s",
		verb,
		quoteIdentifier(name),
		defaultCharsetClause,
		defaultCollationClause,
	)
}

func readDatabase(ctx context.Context, db *sql.DB, d *DatabaseResourceModel) error {

	sql := "SELECT SCHEMA_NAME,DEFAULT_CHARACTER_SET_NAME,DEFAULT_COLLATION_NAME" +
		" FROM information_schema.SCHEMATA" +
		fmt.Sprintf(" WHERE SCHEMA_NAME='%s'", d.Database.ValueString())
	tflog.Debug(ctx, fmt.Sprintf("readDatabase SQL: %s", sql))

	rows, err := db.QueryContext(ctx, sql)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var database_name, default_character_set, default_collation string

		if err := rows.Scan(&database_name, &default_character_set, &default_collation); err != nil {
			return err
		}

		d.Database = types.StringValue(database_name)
		d.DefaultCharacterSet = types.StringValue(default_character_set)
		d.DefaultCollation = types.StringValue(default_collation)
	}

	return nil
}
