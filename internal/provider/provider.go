// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"golang.org/x/net/proxy"
)

// Ensure MysqlProvider satisfies various provider interfaces.
var _ provider.Provider = &MysqlProvider{}
var _ provider.ProviderWithFunctions = &MysqlProvider{}
var _ provider.ProviderWithEphemeralResources = &MysqlProvider{}

// MysqlProvider defines the provider implementation.
type MysqlProvider struct {
	// version is set to the provider version on release, "dev" when the
	// provider is built and ran locally, and "test" when running acceptance
	// testing.
	version string
}

const (
	cleartextPasswords  = "cleartext"
	nativePasswords     = "native"
	userNotFoundErrCode = 1133
	unknownUserErrCode  = 1396
	azEnvPublic         = "public"
	azEnvChina          = "china"
	azEnvGerman         = "german"
	azEnvUSGovernment   = "usgovernment"
)

type MySQLConfiguration struct {
	Config                 *mysql.Config
	MaxConnLifetime        time.Duration
	MaxOpenConns           int64
	ConnectRetryTimeoutSec time.Duration
}

type CustomTLS struct {
	ConfigKey  types.String `tfsdk:"config_key"`
	CACert     types.String `tfsdk:"ca_cert"`
	ClientCert types.String `tfsdk:"client_cert"`
	ClientKey  types.String `tfsdk:"client_key"`
}

// MysqlProviderModel describes the provider data model.
type MysqlProviderModel struct {
	Endpoint               types.String      `tfsdk:"endpoint"`
	Username               types.String      `tfsdk:"username"`
	Password               types.String      `tfsdk:"password"`
	AuthenticationPlugin   types.String      `tfsdk:"authentication_plugin"`
	Proxy                  types.String      `tfsdk:"proxy"`
	TLS                    types.String      `tfsdk:"tls"`
	ConnParams             map[string]string `tfsdk:"conn_params"`
	MaxConnLifetimeSec     types.Int64       `tfsdk:"max_conn_lifetime_sec"`
	ConnectRetryTimeoutSec types.Int64       `tfsdk:"connect_retry_timeout_sec"`
	MaxOpenConns           types.Int64       `tfsdk:"max_open_conns"`

	// CustomTLS              CustomTLS         `tfsdk:"custom_tls"`
}

type OneConnection struct {
	Db      *sql.DB
	Version *version.Version
}

var (
	connectionCacheMtx sync.Mutex
	connectionCache    map[string]*OneConnection
)

func init() {
	connectionCacheMtx.Lock()
	defer connectionCacheMtx.Unlock()

	connectionCache = map[string]*OneConnection{}
}

func (p *MysqlProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "mysql"
	resp.Version = p.version
}

func (p *MysqlProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Description: "Endpoint for MySQL. May also be provided via MYSQL_ENDPOINT environment variable.",
				Optional:    true,
			},
			"username": schema.StringAttribute{
				Description: "Username for MySQL. May also be provided via MYSQL_USERNAME environment variable.",
				Optional:    true,
			},
			"password": schema.StringAttribute{
				Description: "Password for MySQL. May also be provided via MYSQL_PASSWORD environment variable.",
				Optional:    true,
				Sensitive:   true,
			},
			"proxy": schema.StringAttribute{
				Description: "Proxy for MySQL.",
				Optional:    true,
			},
			"tls": schema.StringAttribute{
				Description: "TLS for MySQL.",
				Optional:    true,
			},
			// "custom_tls": schema.MapNestedAttribute{
			// 	Description: "custom_tls for MySQL.",
			// 	Optional:    true,
			// 	NestedObject: schema.NestedAttributeObject{
			// 		Attributes: map[string]schema.Attribute{
			// 			"config_key": schema.StringAttribute{
			// 				Description: "",
			// 				Optional:    true,
			// 			},
			// 			"ca_cert": schema.StringAttribute{
			// 				Description: "",
			// 				Optional:    true,
			// 			},
			// 			"client_cert": schema.StringAttribute{
			// 				Description: "",
			// 				Optional:    true,
			// 			},
			// 			"client_key": schema.StringAttribute{
			// 				Description: "",
			// 				Optional:    true,
			// 			},
			// 		},
			// 	},
			// },
			"conn_params": schema.MapAttribute{
				Description: "conn_params for MySQL.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"max_conn_lifetime_sec": schema.Int64Attribute{
				Description: "Max conn lifetime(sec) for MySQL.",
				Optional:    true,
			},
			"connect_retry_timeout_sec": schema.Int64Attribute{
				Description: "Connect retry timeout(sec) for MySQL.",
				Optional:    true,
			},
			"max_open_conns": schema.Int64Attribute{
				Description: "Max open conns for MySQL.",
				Optional:    true,
			},
			"authentication_plugin": schema.StringAttribute{
				Description: "Authentication plugin for MySQL.",
				Optional:    true,
			},
		},
	}
}

func (p *MysqlProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	tflog.Info(ctx, "Configuring MySQL")
	// Retrieve provider data from configuration
	var config MysqlProviderModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Debug(ctx, fmt.Sprintf("config: %+v", config))

	endpoint := os.Getenv("MYSQL_ENDPOINT")
	username := os.Getenv("MYSQL_USERNAME")
	password := os.Getenv("MYSQL_PASSWORD")

	if !config.Endpoint.IsNull() {
		endpoint = config.Endpoint.ValueString()
	}

	if !config.Username.IsNull() {
		username = config.Username.ValueString()
	}

	if !config.Password.IsNull() {
		password = config.Password.ValueString()
	}

	ctx = tflog.SetField(ctx, "mysql_endpoint", endpoint)
	ctx = tflog.SetField(ctx, "mysql_username", username)
	ctx = tflog.SetField(ctx, "mysql_password", password)
	ctx = tflog.MaskFieldValuesWithFieldKeys(ctx, "mysql_password")

	proto := "tcp"
	if len(endpoint) > 0 && endpoint[0] == '/' {
		proto = "unix"
	}

	var authPlugin string
	if !config.AuthenticationPlugin.IsNull() {
		authPlugin = config.AuthenticationPlugin.ValueString()
	}
	var allowClearTextPasswords = authPlugin == cleartextPasswords
	var allowNativePasswords = authPlugin == nativePasswords
	var maxConnLifetimeSec int64
	if !config.MaxConnLifetimeSec.IsNull() {
		maxConnLifetimeSec = config.MaxConnLifetimeSec.ValueInt64()
	}
	var connectRetryTimeoutSec int64
	if !config.MaxConnLifetimeSec.IsNull() {
		connectRetryTimeoutSec = config.ConnectRetryTimeoutSec.ValueInt64()
	}
	var maxOpenConns int64
	if !config.MaxConnLifetimeSec.IsNull() {
		maxOpenConns = config.MaxOpenConns.ValueInt64()
	}
	var tlsConfig = "false"
	if !config.TLS.IsNull() {
		tlsConfig = config.TLS.ValueString()
	}
	var tlsConfigStruct *tls.Config
	// configKey := "default"
	// if !config.CustomTLS.ConfigKey.IsNull() {
	// 	configKey = config.CustomTLS.ConfigKey.ValueString()

	// 	tlsConfigStruct = &tls.Config{}
	// 	var pem []byte
	// 	if !config.CustomTLS.CACert.IsNull() {
	// 		caCert := config.CustomTLS.CACert.ValueString()
	// 		tflog.Debug(ctx, "Using custom CA cert")
	// 		rootCertPool := x509.NewCertPool()
	// 		if strings.HasPrefix(caCert, "-----BEGIN") {
	// 			pem = []byte(caCert)
	// 		} else {
	// 			_pem, err := os.ReadFile(caCert)
	// 			if err != nil {
	// 				resp.Diagnostics.AddError("failed to read CA cert", err.Error())
	// 				return
	// 			}
	// 			pem = _pem
	// 		}
	// 		if ok := rootCertPool.AppendCertsFromPEM(pem); !ok {
	// 			resp.Diagnostics.AddError("failed to append pem", string(pem))
	// 			return
	// 		}
	// 		tlsConfigStruct.RootCAs = rootCertPool
	// 	}

	// 	if !config.CustomTLS.ClientCert.IsNull() && !config.CustomTLS.ClientKey.IsNull() {
	// 		tflog.Debug(ctx, "Using custom ClientCert & ClientKey")
	// 		clientCert := config.CustomTLS.ClientCert.ValueString()
	// 		clientKey := config.CustomTLS.ClientKey.ValueString()
	// 		var cert tls.Certificate
	// 		var err error
	// 		if strings.HasPrefix(clientCert, "-----BEGIN") {
	// 			cert, err = tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
	// 		} else {
	// 			cert, err = tls.LoadX509KeyPair(clientCert, clientKey)
	// 		}
	// 		if err != nil {
	// 			resp.Diagnostics.AddError("error loading keypair", err.Error())
	// 			return
	// 		}
	// 		tlsConfigStruct.Certificates = []tls.Certificate{cert}
	// 	}

	// 	// Register the config
	// 	err := mysql.RegisterTLSConfig(configKey, tlsConfigStruct)
	// 	if err != nil {
	// 		resp.Diagnostics.AddError("failed registering TLS config", err.Error())
	// 		return
	// 	}
	// 	tlsConfig = configKey
	// }

	conf := mysql.Config{
		User:                    username,
		Passwd:                  password,
		Net:                     proto,
		Addr:                    endpoint,
		TLSConfig:               tlsConfig,
		AllowNativePasswords:    allowNativePasswords,
		AllowCleartextPasswords: allowClearTextPasswords,
		InterpolateParams:       true,
		Params:                  config.ConnParams,
	}

	if tlsConfigStruct != nil {
		conf.TLS = tlsConfigStruct
	}

	tflog.Debug(ctx, "Creating Proxy Dialer")

	dialer, err := makeDialer(config.Proxy.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("failed making dialer", err.Error())
		return
	}

	mysql.RegisterDialContext("tcp", func(ctx context.Context, network string) (net.Conn, error) {
		return dialer.Dial("tcp", network)
	})

	mysqlConf := &MySQLConfiguration{
		Config:                 &conf,
		MaxConnLifetime:        time.Duration(maxConnLifetimeSec) * time.Second,
		MaxOpenConns:           maxOpenConns,
		ConnectRetryTimeoutSec: time.Duration(connectRetryTimeoutSec) * time.Second,
	}

	// Make the HashiCups client available during DataSource and Resource
	// type Configure methods.
	resp.DataSourceData = mysqlConf
	resp.ResourceData = mysqlConf

	tflog.Info(ctx, "Configured MySQL", map[string]any{"success": true})
}

func (p *MysqlProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewDatabaseResource,
		NewUserResource,
	}
}

func (p *MysqlProvider) EphemeralResources(ctx context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		NewExampleEphemeralResource,
	}
}

func (p *MysqlProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewDatabasesDataSource,
		NewTablesDataSource,
	}
}

func (p *MysqlProvider) Functions(ctx context.Context) []func() function.Function {
	return []func() function.Function{
		NewExampleFunction,
	}
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &MysqlProvider{
			version: version,
		}
	}
}

func afterConnectVersion(ctx context.Context, mysqlConf *MySQLConfiguration, db *sql.DB) (*version.Version, error) {
	// Set up env so that we won't create users randomly.
	currentVersion, err := serverVersion(db)
	if err != nil {
		return nil, fmt.Errorf("failed getting server version: %v", err)
	}

	versionMinInclusive, _ := version.NewVersion("5.7.5")
	versionMaxExclusive, _ := version.NewVersion("8.0.0")
	if currentVersion.GreaterThanOrEqual(versionMinInclusive) &&
		currentVersion.LessThan(versionMaxExclusive) {
		// We set NO_AUTO_CREATE_USER to prevent provider from creating user when creating grants. Newer MySQL has it automatically.
		// We don't want any other modes, esp. not ANSI_QUOTES.
		_, err = db.ExecContext(ctx, `SET SESSION sql_mode='NO_AUTO_CREATE_USER'`)
		if err != nil {
			return nil, fmt.Errorf("failed setting SQL mode: %v", err)
		}
	} else {
		// We don't want any modes, esp. not ANSI_QUOTES.
		_, err = db.ExecContext(ctx, `SET SESSION sql_mode=''`)
		if err != nil {
			return nil, fmt.Errorf("failed setting SQL mode: %v", err)
		}
	}

	return currentVersion, nil
}

var identQuoteReplacer = strings.NewReplacer("`", "``")

func quoteIdentifier(in string) string {
	return fmt.Sprintf("`%s`", identQuoteReplacer.Replace(in))
}

func makeDialer(p string) (proxy.Dialer, error) {
	proxyFromEnv := proxy.FromEnvironment()
	proxyArg := p

	if len(proxyArg) > 0 {
		proxyURL, err := url.Parse(proxyArg)
		if err != nil {
			return nil, err
		}
		proxyDialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			return nil, err
		}

		return proxyDialer, nil
	}

	return proxyFromEnv, nil
}

func serverVersion(db *sql.DB) (*version.Version, error) {
	var versionString string
	err := db.QueryRow("SELECT @@GLOBAL.version").Scan(&versionString)
	if err != nil {
		return nil, err
	}

	versionString = strings.SplitN(versionString, ":", 2)[0]
	return version.NewVersion(versionString)
}

func serverVersionString(db *sql.DB) (string, error) {
	var versionString string
	err := db.QueryRow("SELECT @@GLOBAL.version").Scan(&versionString)
	if err != nil {
		return "", err
	}

	return versionString, nil
}

// serverTiDB returns:
// - it is a TiDB instance
// - tidbVersion
// - mysqlCompatibilityVersion
// - err
func serverTiDB(db *sql.DB) (bool, string, string, error) {
	currentVersionString, err := serverVersionString(db)
	if err != nil {
		return false, "", "", err
	}

	if strings.Contains(currentVersionString, "TiDB") {
		versions := strings.SplitN(currentVersionString, "-", 3)
		return true, versions[2], versions[0], nil
	}

	return false, "", "", nil
}

func serverRds(db *sql.DB) (bool, error) {
	var metadataVersionString string
	err := db.QueryRow("SELECT @@GLOBAL.datadir").Scan(&metadataVersionString)
	if err != nil {
		return false, err
	}

	if strings.Contains(metadataVersionString, "rds") {
		return true, nil
	}

	return false, nil
}

func connectToMySQL(ctx context.Context, conf *MySQLConfiguration) (*sql.DB, error) {
	conn, err := connectToMySQLInternal(ctx, conf)
	if err != nil {
		return nil, err
	}
	return conn.Db, nil
}

func connectToMySQLInternal(ctx context.Context, conf *MySQLConfiguration) (*OneConnection, error) {
	// This is fine - we'll connect serially, but we don't expect more than
	// 1 or 2 connections starting at once.
	connectionCacheMtx.Lock()
	defer connectionCacheMtx.Unlock()

	dsn := conf.Config.FormatDSN()
	log.Printf("[DEBUG] Using dsn: %s", dsn)
	if connectionCache[dsn] != nil {
		return connectionCache[dsn], nil
	}

	connection, err := createNewConnection(ctx, conf)
	if err != nil {
		return nil, fmt.Errorf("could not create new connection: %v", err)
	}

	connectionCache[dsn] = connection
	return connectionCache[dsn], nil
}

func createNewConnection(ctx context.Context, conf *MySQLConfiguration) (*OneConnection, error) {
	var db *sql.DB
	var err error

	driverName := "mysql"
	tflog.Debug(ctx, "Using driverName", map[string]interface{}{"driverName": driverName})

	// When provisioning a database server there can often be a lag between
	// when Terraform thinks it's available and when it is actually available.
	// This is particularly acute when provisioning a server and then immediately
	// trying to provision a database on it.
	retryError := retry.RetryContext(ctx, conf.ConnectRetryTimeoutSec, func() *retry.RetryError {
		db, err = sql.Open(driverName, conf.Config.FormatDSN())
		if err != nil {
			if mysqlErrorNumber(err) != 0 || ctx.Err() != nil {
				return retry.NonRetryableError(err)
			}
			return retry.RetryableError(err)
		}

		err = db.PingContext(ctx)
		if err != nil {
			if mysqlErrorNumber(err) != 0 || ctx.Err() != nil {
				return retry.NonRetryableError(err)
			}

			return retry.RetryableError(err)
		}

		return nil
	})

	if retryError != nil {
		return nil, fmt.Errorf("could not connect to server: %s", retryError)
	}
	db.SetConnMaxLifetime(conf.MaxConnLifetime)

	// We used to set conf.MaxOpenConns, but then some connections are open outside our control
	// and without our settings like no ANSI_QUOTES.
	// TODO: find a way to support more open connections while able to set custom settings for each of them.
	db.SetMaxOpenConns(1)

	currentVersion, err := afterConnectVersion(ctx, conf, db)
	if err != nil {
		return nil, fmt.Errorf("failed running after connect command: %v", err)
	}

	return &OneConnection{
		Db:      db,
		Version: currentVersion,
	}, nil
}
