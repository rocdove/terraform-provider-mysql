terraform {
  required_providers {
    mysql = {
      source  = "petoju/mysql"
      version = "3.0.67"
    }
  }
}

# Configure the MySQL provider
provider "mysql" {
  endpoint = "127.0.0.1:3306"
  username = "root"
  password = "My@root.84"
}

resource "mysql_user" "terraform" {
  # endpoint = "127.0.0.1:3306"
  user = "terraform1"
  host = "%"
  plaintext_password = "My@3306.tf1"
}