terraform {
  required_providers {
    mysql = {
      source = "rocdove/mysql"
    }
  }
}

provider "mysql" {
  username = "root"
  password = "My@root.84"
  max_conn_lifetime_sec = 30
  connect_retry_timeout_sec = 6
  max_open_conns = 10
  authentication_plugin = "native"
  tls = "skip-verify"
  conn_params = {
  }
}

data "mysql_databases" "databases" {
  endpoint = "127.0.0.1:3306"
}

output "databases" {
  value = data.mysql_databases.databases.databases
  # sensitive = true
}

data "mysql_tables" "tables" {
  endpoint = "127.0.0.1:3306"
  database = "mysql"
}

output "tables" {
  value = data.mysql_tables.tables.tables
  # sensitive = true
}