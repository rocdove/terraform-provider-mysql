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

resource "mysql_database" "terraform" {
  endpoint = "127.0.0.1:3306"
  database = "terraform"
  default_character_set = "utf8mb4"
}

resource "mysql_user" "terraform" {
  endpoint = "127.0.0.1:3306"
  user = "terraform"
  host = "%"
  plaintext_password = "My@3306.tf"
}

# data "mysql_tables" "tables" {
#   endpoint = "127.0.0.1:3306"
#   database = "mysql"
# }

# output "tables" {
#   value = data.mysql_tables.tables.tables
#   # sensitive = true
# }

# output "database_terraform" {
#   value = mysql_database.terraform
# }


# data "mysql_databases" "databases" {
#   endpoint = "127.0.0.1:3306"
# }

# output "databases" {
#   value = data.mysql_databases.databases
#   # sensitive = true
# }