// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/jinzhu/copier"
)

// Ensure provider defined types fully satisfy framework interfaces.
var (
	_ resource.Resource                = &UserResource{}
	_ resource.ResourceWithConfigure   = &UserResource{}
	_ resource.ResourceWithImportState = &UserResource{}
)

func NewUserResource() resource.Resource {
	return &UserResource{}
}

// UserResource defines the resource implementation.
type UserResource struct {
	conf *MySQLConfiguration
}

// UserResourceModel describes the resource data model.
type UserResourceModel struct {
	ID                types.String `tfsdk:"id"`
	Endpoint          types.String `tfsdk:"endpoint"`
	User              types.String `tfsdk:"user"`
	Host              types.String `tfsdk:"host"`
	PlaintextPassword types.String `tfsdk:"plaintext_password"`
	RetainOldPassword types.Bool   `tfsdk:"retain_old_password"`
	AuthPlugin        types.String `tfsdk:"auth_plugin"`
	AuthStringHashed  types.String `tfsdk:"auth_string_hashed"`
	TLSOption         types.String `tfsdk:"tls_option"`
}

func (r *UserResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

// authPlanModifier is an plan modifier that sets RequiresReplace
// on the attribute if a given function is true.
type authPlanModifier struct {
	description         string
	markdownDescription string
}

// Description returns a human-readable description of the plan modifier.
func (m authPlanModifier) Description(_ context.Context) string {
	return m.description
}

// MarkdownDescription returns a markdown description of the plan modifier.
func (m authPlanModifier) MarkdownDescription(_ context.Context) string {
	return m.markdownDescription
}

// PlanModifyString implements the plan modification logic.
func (m authPlanModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	tflog.Debug(ctx, fmt.Sprintf("req: %+v", req))
	var plan UserResourceModel
	var state UserResourceModel

	// Read Terraform plan plan into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.PlaintextPassword.Equal(state.PlaintextPassword) {
		resp.PlanValue = req.StateValue
	}
}

func (r *UserResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// This description is used by the documentation generator and the language server.
		MarkdownDescription: "User resource",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "endpoint, user and host create to id(endpoint/user@host).",
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
			"user": schema.StringAttribute{
				MarkdownDescription: "user name.",
				Required:            true,
			},
			"host": schema.StringAttribute{
				MarkdownDescription: "host for the user.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("%"),
			},
			"plaintext_password": schema.StringAttribute{
				MarkdownDescription: "plaintext_password for the user.",
				Optional:            true,
				Sensitive:           true,
				Validators:          []validator.String{},
			},
			"retain_old_password": schema.BoolAttribute{
				MarkdownDescription: "retain_old_password for the user.",
				Optional:            true,
			},
			"auth_plugin": schema.StringAttribute{
				MarkdownDescription: "auth_plugin for the user.",
				Optional:            true,
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.ConflictsWith(path.Expressions{
						path.MatchRoot("plaintext_password"),
					}...),
				},
				PlanModifiers: []planmodifier.String{
					authPlanModifier{
						description:         "auth_plugin null is default",
						markdownDescription: "auth_plugin null is default",
					},
				},
			},
			"auth_string_hashed": schema.StringAttribute{
				MarkdownDescription: "auth_string_hashed for the user.",
				Optional:            true,
				Sensitive:           true,
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.ConflictsWith(path.Expressions{
						path.MatchRoot("plaintext_password"),
					}...),
				},
				PlanModifiers: []planmodifier.String{
					authPlanModifier{
						description:         "auth_string_hashed null is default",
						markdownDescription: "auth_string_hashed null is default",
					},
				},
			},
			"tls_option": schema.StringAttribute{
				MarkdownDescription: "tls_option for the user.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Default: stringdefault.StaticString("NONE"),
			},
		},
	}
}

func (r *UserResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
	tflog.Debug(ctx, fmt.Sprintf("user_data_source Configure conf: %+v", *conf))

	r.conf = conf
}

// 实现 ConfigValidators 接口
// func (r *UserResource) ConfigValidators(ctx context.Context) []resource.ConfigValidator {
// 	return []resource.ConfigValidator{
// 		// 定义 attribute_a 和 attribute_b 互斥
// 		resourcevalidator.Conflicting(
// 			path.MatchRoot("attribute_a"),
// 			path.MatchRoot("attribute_b"),
// 		),
// 	}
// }

func (r *UserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan UserResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)

	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Debug(ctx, fmt.Sprintf("UserResourceModel: %+v", plan))

	var conf MySQLConfiguration
	err := copier.Copy(&conf, r.conf)
	if err != nil {
		resp.Diagnostics.AddError("Error copying struct:", err.Error())
		return
	}

	if !plan.Endpoint.IsUnknown() {
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
	var authStm string
	var auth string
	var createObj = "USER"

	if !plan.AuthPlugin.IsUnknown() {
		auth = plan.AuthPlugin.ValueString()
	}

	if len(auth) > 0 {
		if auth == "aad_auth" {
			// aad_auth is plugin but Microsoft uses another statement to create this kind of users
			// createObj = "AADUSER"
			// if _, ok := d.GetOk("aad_identity"); !ok {
			// 	return diag.Errorf("aad_identity is required for aad_auth")
			// }
			resp.Diagnostics.AddError("aad_auth is not supported", "Add support later.")
			return
		} else if auth == "AWSAuthenticationPlugin" {
			authStm = " IDENTIFIED WITH AWSAuthenticationPlugin as 'RDS'"
		} else {
			// mysql_no_login, auth_pam, ...
			authStm = " IDENTIFIED WITH " + auth
		}
	}
	if !plan.AuthStringHashed.IsUnknown() {
		hashed := plan.AuthStringHashed.ValueString()
		if hashed != "" {
			if authStm == "" {
				resp.Diagnostics.AddError("auth_string_hashed is not supported",
					fmt.Sprintf("auth_string_hashed is not supported for auth plugin %s", auth))
				return
			}
			authStm = fmt.Sprintf("%s AS '%s'", authStm, hashed)
		}
	}

	var stmtSQL string

	if createObj == "AADUSER" {
		// var aadIdentity = d.Get("aad_identity").(*schema.Set).List()[0].(map[string]interface{})

		// if aadIdentity["type"].(string) == "service_principal" {
		// 	// CREATE AADUSER 'mysqlProtocolLoginName"@"mysqlHostRestriction' IDENTIFIED BY 'identityId'
		// 	stmtSQL = fmt.Sprintf("CREATE AADUSER '%s'@'%s' IDENTIFIED BY '%s'",
		// 		d.Get("user").(string),
		// 		d.Get("host").(string),
		// 		aadIdentity["identity"].(string))
		// } else {
		// 	// CREATE AADUSER 'identityName"@"mysqlHostRestriction' AS 'mysqlProtocolLoginName'
		// 	stmtSQL = fmt.Sprintf("CREATE AADUSER '%s'@'%s' AS '%s'",
		// 		aadIdentity["identity"].(string),
		// 		d.Get("host").(string),
		// 		d.Get("user").(string))
		// }
		resp.Diagnostics.AddError("AADUSER is not supported", "Add support later.")
		return
	} else {
		stmtSQL = fmt.Sprintf("CREATE USER '%s'@'%s'", plan.User.ValueString(), plan.Host.ValueString())
	}

	var password string
	if !plan.PlaintextPassword.IsNull() {
		password = plan.PlaintextPassword.ValueString()
	}
	tflog.Debug(ctx, fmt.Sprintf("add password: %s", password))

	if auth == "AWSAuthenticationPlugin" && plan.Host.ValueString() == "localhost" {
		resp.Diagnostics.AddError("cannot use IAM auth against localhost", "")
		return
	}

	if authStm != "" {
		stmtSQL = stmtSQL + authStm
		if password != "" {
			stmtSQL = stmtSQL + fmt.Sprintf(" BY '%s'", password)
		}
	} else if password != "" {
		stmtSQL = stmtSQL + fmt.Sprintf(" IDENTIFIED BY '%s'", password)
	}

	requiredVersion, _ := version.NewVersion("5.7.0")
	var updateStmtSql = ""
	if getVersionFromMeta(ctx, &conf).GreaterThan(requiredVersion) && !plan.TLSOption.IsNull() {
		tflog.Debug(ctx, fmt.Sprintf("add tls_option: %s", plan.TLSOption.ValueString()))
		if createObj == "AADUSER" {
			updateStmtSql = fmt.Sprintf("ALTER USER '%s'@'%s' REQUIRE %s",
				plan.User.ValueString(),
				plan.Host.ValueString(),
				plan.TLSOption.ValueString())
			resp.Diagnostics.AddError("AADUSER is not supported", "Add support later.")
			return
		} else {
			stmtSQL += fmt.Sprintf(" REQUIRE %s", plan.TLSOption.ValueString())
		}
	}

	retainPassword := plan.RetainOldPassword.ValueBool()
	if retainPassword {
		err := checkRetainCurrentPasswordSupport(ctx, &conf)
		if err != nil {
			tflog.Error(ctx, fmt.Sprintf("cannot use retain_current_password: %s", err.Error()))
			resp.Diagnostics.AddError("cannot use retain_current_password", err.Error())
			return
		}
	}

	tflog.Debug(ctx, fmt.Sprintf("Executing statement: %s", stmtSQL))
	_, err = db.ExecContext(ctx, stmtSQL)
	if err != nil {
		tflog.Error(ctx, fmt.Sprintf("failed executing SQL: %s", err.Error()))
		resp.Diagnostics.AddError("failed executing SQL: %s", err.Error())
		return
	}

	if updateStmtSql != "" {
		tflog.Debug(ctx, fmt.Sprintf("Executing statement: %s", updateStmtSql))
		_, err = db.ExecContext(ctx, updateStmtSql)
		if err != nil {
			plan.TLSOption = types.StringValue("")
			tflog.Error(ctx, fmt.Sprintf("failed executing SQL: %s", err.Error()))
			resp.Diagnostics.AddError("failed executing SQL: %s", err.Error())
			return
		}
	}

	plan.ID = types.StringValue(fmt.Sprintf("%s/%s@'%s'", conf.Config.Addr, plan.User.ValueString(), plan.Host.ValueString()))

	// Write logs using the tflog package
	// Documentation: https://terraform.io/plugin/log
	tflog.Trace(ctx, "created a user resource")

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *UserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state UserResourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Debug(ctx, fmt.Sprintf("UserResourceModel: %+v", state))

	id := state.ID.ValueString()
	if state.User.IsNull() && strings.Contains(id, "/") {
		parts := strings.Split(id, "/")
		state.Endpoint = types.StringValue(parts[0])
		if strings.Contains(parts[1], "@") {
			userParts := strings.Split(parts[1], "@")
			state.User = types.StringValue(userParts[0])
			state.Host = types.StringValue(userParts[1])
		} else {
			state.User = types.StringValue(parts[1])
		}
	}
	tflog.Debug(ctx, fmt.Sprintf("UserResourceModel: %+v", state))

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

	err = readUser(ctx, db, &state, &conf)
	if err != nil {
		resp.Diagnostics.AddError("Read User Error", err.Error())
		return
	}
	state.ID = types.StringValue(fmt.Sprintf("%s/%s@'%s'", conf.Config.Addr, state.User.ValueString(), state.Host.ValueString()))

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *UserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan UserResourceModel
	var state UserResourceModel

	// Read Terraform plan plan into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Debug(ctx, fmt.Sprintf("UserResourceModel plan: %+v, state: %+v", plan, state))

	var conf MySQLConfiguration
	err := copier.Copy(&conf, r.conf)
	if err != nil {
		resp.Diagnostics.AddError("Error copying struct:", err.Error())
		return
	}

	if !plan.Endpoint.IsUnknown() {
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

	var auth string
	if !plan.AuthPlugin.IsUnknown() {
		auth = plan.AuthPlugin.ValueString()
	}

	if len(auth) > 0 {
		if !plan.TLSOption.Equal(state.TLSOption) || !plan.AuthPlugin.Equal(state.AuthPlugin) || !plan.AuthStringHashed.Equal(state.AuthStringHashed) {
			var stmtSQL string

			authString := ""
			if !plan.AuthStringHashed.IsNull() {
				authString = fmt.Sprintf("IDENTIFIED WITH %s AS '%s'", plan.AuthPlugin.ValueString(), plan.AuthStringHashed.ValueString())
			}
			stmtSQL = fmt.Sprintf("ALTER USER '%s'@'%s' %s  REQUIRE %s",
				plan.User.ValueString(),
				plan.Host.ValueString(),
				authString,
				plan.TLSOption.ValueString())

			tflog.Debug(ctx, fmt.Sprintf("Executing query: %s", stmtSQL))
			_, err := db.ExecContext(ctx, stmtSQL)
			if err != nil {
				tflog.Error(ctx, fmt.Sprintf("failed executing SQL: %s", err.Error()))
				resp.Diagnostics.AddError("failed executing SQL: %s", err.Error())
				return
			}
		}
	}

	var newPassword string
	if !plan.PlaintextPassword.Equal(state.PlaintextPassword) {
		newPassword = plan.PlaintextPassword.ValueString()
	} else {
		newPassword = ""
	}

	retainPassword := plan.RetainOldPassword.ValueBool()
	if retainPassword {
		err := checkRetainCurrentPasswordSupport(ctx, &conf)
		if err != nil {
			tflog.Error(ctx, fmt.Sprintf("cannot use retain_current_password: %s", err.Error()))
			resp.Diagnostics.AddError("cannot use retain_current_password", err.Error())
			return
		}
	}

	if newPassword != "" {
		stmtSQL, err := getSetPasswordStatement(ctx, &conf, retainPassword)
		if err != nil {
			tflog.Error(ctx, fmt.Sprintf("failed getting change password statement: %s", err.Error()))
			resp.Diagnostics.AddError("failed getting change password statement", err.Error())
			return
		}

		tflog.Debug(ctx, fmt.Sprintf("Executing query: %s", stmtSQL))
		_, err = db.ExecContext(ctx, stmtSQL,
			plan.User.ValueString(),
			plan.Host.ValueString(),
			newPassword)
		if err != nil {
			tflog.Error(ctx, fmt.Sprintf("failed changing password: %s", err.Error()))
			resp.Diagnostics.AddError("failed changing password", err.Error())
			return
		}
	}

	requiredVersion, _ := version.NewVersion("5.7.0")
	if !plan.TLSOption.IsUnknown() && !plan.TLSOption.Equal(state.TLSOption) &&
		getVersionFromMeta(ctx, &conf).GreaterThan(requiredVersion) {
		stmtSQL := fmt.Sprintf("ALTER USER '%s'@'%s' REQUIRE %s",
			plan.User.ValueString(),
			plan.Host.ValueString(),
			plan.TLSOption.ValueString())

		tflog.Debug(ctx, fmt.Sprintf("Executing query: %s", stmtSQL))
		_, err := db.ExecContext(ctx, stmtSQL)
		if err != nil {
			tflog.Error(ctx, fmt.Sprintf("failed setting require tls option: %s", err.Error()))
			resp.Diagnostics.AddError("failed setting require tls option", err.Error())
			return
		}
	}
	err = readUser(ctx, db, &plan, &conf)
	if err != nil {
		resp.Diagnostics.AddError("Read User Error", err.Error())
		return
	}

	plan.ID = types.StringValue(fmt.Sprintf("%s/%s@'%s'", conf.Config.Addr, plan.User.ValueString(), plan.Host.ValueString()))
	// Save updated plan into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *UserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state UserResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)

	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Debug(ctx, fmt.Sprintf("UserResourceModel: %+v", state))

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

	stmtSQL := "DROP USER ?@?"

	tflog.Debug(ctx, fmt.Sprintf("Executing statement: %s", stmtSQL))

	_, err = db.ExecContext(ctx, stmtSQL,
		state.User.ValueString(),
		state.Host.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("failed running SQL to drop user", err.Error())
		return
	}
}

func (r *UserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func checkRetainCurrentPasswordSupport(ctx context.Context, meta interface{}) error {
	ver, _ := version.NewVersion("8.0.14")
	if getVersionFromMeta(ctx, meta).LessThan(ver) {
		return errors.New("MySQL version must be at least 8.0.14")
	}
	return nil
}

func getSetPasswordStatement(ctx context.Context, meta interface{}, retainPassword bool) (string, error) {
	if retainPassword {
		return "ALTER USER ?@? IDENTIFIED BY ? RETAIN CURRENT PASSWORD", nil
	}

	/* ALTER USER syntax introduced in MySQL 5.7.6 deprecates SET PASSWORD (GH-8230) */
	ver, _ := version.NewVersion("5.7.6")
	if getVersionFromMeta(ctx, meta).LessThan(ver) {
		return "SET PASSWORD FOR ?@? = PASSWORD(?)", nil
	}

	return "ALTER USER ?@? IDENTIFIED BY ?", nil
}

func readUser(ctx context.Context, db *sql.DB, d *UserResourceModel, conf *MySQLConfiguration) error {
	requiredVersion, _ := version.NewVersion("5.7.0")
	if getVersionFromMeta(ctx, conf).GreaterThan(requiredVersion) {
		stmt := "SHOW CREATE USER ?@?"

		var createUserStmt string
		err := db.QueryRowContext(ctx, stmt, d.User.ValueString(), d.Host.ValueString()).Scan(&createUserStmt)
		if err != nil {
			errorNumber := mysqlErrorNumber(err)
			if errorNumber == unknownUserErrCode || errorNumber == userNotFoundErrCode {
				d.ID = types.StringValue("")
				// return nil
			}
			tflog.Error(ctx, fmt.Sprintf("Faild Getting User: %s", err.Error()))
			return err
		}
		tflog.Debug(ctx, fmt.Sprintf("createUserStmt: %s", createUserStmt))

		// Examples of create user:
		// CREATE USER 'some_app'@'%' IDENTIFIED WITH 'mysql_native_password' AS '*0something' REQUIRE NONE PASSWORD EXPIRE DEFAULT ACCOUNT UNLOCK
		// CREATE USER `jdoe-tf-test-47`@`example.com` IDENTIFIED WITH 'caching_sha2_password' REQUIRE NONE PASSWORD EXPIRE DEFAULT ACCOUNT UNLOCK PASSWORD HISTORY DEFAULT PASSWORD REUSE INTERVAL DEFAULT PASSWORD REQUIRE CURRENT DEFAULT
		// CREATE USER `jdoe`@`example.com` IDENTIFIED WITH 'caching_sha2_password' AS '$A$005$i`xay#fG/\' TrbkNA82' REQUIRE NONE PASSWORD
		re := regexp.MustCompile("^CREATE USER ['`]([^'`]*)['`]@['`]([^'`]*)['`] IDENTIFIED WITH ['`]([^'`]*)['`] (?:AS '((?:.*?[^\\\\])?)' )?REQUIRE ([^ ]*)")
		if m := re.FindStringSubmatch(createUserStmt); len(m) == 6 {
			tflog.Debug(ctx, fmt.Sprintf("regexp result: %s", m))
			d.User = types.StringValue(m[1])
			d.Host = types.StringValue(m[2])
			d.AuthPlugin = types.StringValue(m[3])
			d.TLSOption = types.StringValue(m[5])

			d.AuthStringHashed = types.StringValue(m[4])
			// if m[3] == "aad_auth" {
			// 	// AADGroup:98e61c8d-e104-4f8c-b1a6-7ae873617fe6:upn:Doe_Family_Group
			// 	// AADUser:98e61c8d-e104-4f8c-b1a6-7ae873617fe6:upn:little.johny@does.onmicrosoft.com
			// 	// AADSP:98e61c8d-e104-4f8c-b1a6-7ae873617fe6:upn:mysqlUserName - for MySQL Flexible Server
			// 	// AADApp:98e61c8d-e104-4f8c-b1a6-7ae873617fe6:upn:mysqlUserName - for MySQL Single Server
			// 	parts := strings.Split(m[4], ":")
			// 	if parts[0] == "AADSP" || parts[0] == "AADApp" {
			// 		// service principals are referenced by UUID only
			// 		d.Set("aad_identity", []map[string]interface{}{
			// 			{
			// 				"type":     "service_principal",
			// 				"identity": parts[1],
			// 			},
			// 		})
			// 	} else if len(parts) >= 4 {
			// 		// users and groups should be referenced by UPN / group name
			// 		if parts[0] == "AADUser" {
			// 			d.Set("aad_identity", []map[string]interface{}{
			// 				{
			// 					"type":     "user",
			// 					"identity": strings.Join(parts[3:], ":"),
			// 				},
			// 			})
			// 		} else {
			// 			d.Set("aad_identity", []map[string]interface{}{
			// 				{
			// 					"type":     "group",
			// 					"identity": strings.Join(parts[3:], ":"),
			// 				},
			// 			})
			// 		}
			// 	} else {
			// 		return diag.Errorf("AAD identity couldn't be parsed - it is %s", m[4])
			// 	}
			// } else {
			// 	d.Set("auth_string_hashed", m[4])
			// }
			return nil
		}

		// Try 2 - just whether the user is there.
		re2 := regexp.MustCompile("^CREATE USER")
		if m := re2.FindStringSubmatch(createUserStmt); m != nil {
			// Ok, we have at least something - it's probably in MariaDB.
			return nil
		}
		tflog.Error(ctx, fmt.Sprintf("Create user couldn't be parsed - it is %s", createUserStmt))
		return fmt.Errorf("Create user couldn't be parsed - it is %s", createUserStmt)
	} else {
		// Worse user detection, only for compat with MySQL 5.6
		stmtSQL := fmt.Sprintf("SELECT USER FROM mysql.user WHERE USER='%s'",
			d.User.ValueString())

		tflog.Debug(ctx, fmt.Sprintf("Executing statement: %s", stmtSQL))

		rows, err := db.QueryContext(ctx, stmtSQL)
		if err != nil {
			return err
		}
		defer rows.Close()

		if !rows.Next() && rows.Err() == nil {
			d.ID = types.StringValue("")
			return nil
		}
		if rows.Err() != nil {
			return rows.Err()
		}
	}

	return nil
}
