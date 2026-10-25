variable "project_name" {
  type    = string
  default = "zotero-webdav-server"
}

variable "env" {
  type    = string
  default = "prod"
}

variable "aws_region" {
  type    = string
  default = "us-east-2"
}

variable "presigned_get_expiration_seconds" {
  description = "Expiration time for presigned S3 download URLs."
  type        = number
  default     = 300

  validation {
    condition = (
      var.presigned_get_expiration_seconds >= 60 &&
      var.presigned_get_expiration_seconds <= 3600
    )

    error_message = "Presigned GET expiration must be between 60 and 3600 seconds."
  }
}

variable "presigned_put_expiration_seconds" {
  description = "Expiration time for presigned S3 upload URLs (short-lived Worker bridge grants)."
  type        = number
  default     = 120

  validation {
    condition = (
      var.presigned_put_expiration_seconds >= 60 &&
      var.presigned_put_expiration_seconds <= 300
    )

    error_message = "Presigned PUT expiration must be between 60 and 300 seconds."
  }
}

variable "lambda_memory_size" {
  description = "Memory allocated to the WebDAV Lambda."
  type        = number
  default     = 128
}

variable "lambda_timeout_seconds" {
  description = "WebDAV Lambda timeout."
  type        = number
  default     = 15
}

variable "api_throttling_burst_limit" {
  description = "Maximum API Gateway burst request count."
  type        = number
  default     = 50
}

variable "api_throttling_rate_limit" {
  description = "Sustained API Gateway requests per second."
  type        = number
  default     = 20
}

variable "api_log_retention_days" {
  description = "CloudWatch access-log retention."
  type        = number
  default     = 30
}

variable "lambda_log_retention_days" {
  description = "CloudWatch Lambda-log retention."
  type        = number
  default     = 30
}

variable "webdav_username" {
  type      = string
  sensitive = true
}

variable "webdav_password" {
  type      = string
  sensitive = true

  validation {
    condition     = length(var.webdav_password) >= 5
    error_message = "The WebDAV password must contain at least 5 characters."
  }
}
variable "webdav_worker_token" {
  description = "Shared Worker-to-Lambda bridge secret (at least 32 characters). Stored in Terraform state: protect state access."
  type        = string
  sensitive   = true

  validation {
    condition     = length(var.webdav_worker_token) >= 32
    error_message = "webdav_worker_token must contain at least 32 characters."
  }
}

variable "cloudflare_api_token" {
  description = "Cloudflare API Token with Workers Scripts Edit permission. Can also be set via CLOUDFLARE_API_TOKEN environment variable."
  type        = string
  sensitive   = true
  default     = null
}

variable "cloudflare_account_id" {
  description = "Cloudflare Account ID."
  type        = string
}

variable "cloudflare_workers_subdomain" {
  description = "Existing Cloudflare account workers.dev subdomain, without .workers.dev."
  type        = string
  default     = "zotero"
}

variable "cloudflare_worker_name" {
  description = "Name of the Cloudflare Worker script."
  type        = string
  default     = "webdav"
}

variable "cloudflare_upload_mode" {
  description = "stream is the default; redirect requires successful Zotero + deployed ingress validation."
  type        = string
  default     = "stream"

  validation {
    condition     = contains(["stream", "redirect"], var.cloudflare_upload_mode)
    error_message = "Use stream or redirect."
  }
}

variable "cloudflare_max_upload_bytes" {
  description = "Application cap, not a Cloudflare quota override. Default conservative Free/Pro cap."
  type        = number
  default     = 100000000

  validation {
    condition     = var.cloudflare_max_upload_bytes > 0 && var.cloudflare_max_upload_bytes <= 5000000000 && floor(var.cloudflare_max_upload_bytes) == var.cloudflare_max_upload_bytes
    error_message = "Use an integer from 1 to 5000000000 within the actual Cloudflare plan limit."
  }
}
