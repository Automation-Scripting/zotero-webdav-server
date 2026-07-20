terraform {
  cloud {
    organization = "Graduate-APPs-USP"

    workspaces {
      name = "webdav-server"
    }
  }

  required_version = ">= 1.6.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
}
