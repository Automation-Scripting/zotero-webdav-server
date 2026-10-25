terraform {
  cloud {
    organization = "Graduate-APPs-USP"

    workspaces {
      name = "zotero-webdav-server"
    }
  }

  required_version = ">= 1.6.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.0"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.24.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
}

# Uses cloudflare_api_token variable if set, otherwise falls back to CLOUDFLARE_API_TOKEN env var.
provider "cloudflare" {
  api_token = var.cloudflare_api_token
}
