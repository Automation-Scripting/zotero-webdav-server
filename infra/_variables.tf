variable "project_name" {
  type        = string
  default     = "webdav-server"
}

variable "env" {
  type        = string
  default     = "prod"
}

variable "aws_region" {
  type        = string
  default     = "us-east-2"
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
  description = "Expiration time for presigned S3 upload URLs."
  type        = number
  default     = 300

  validation {
    condition = (
      var.presigned_put_expiration_seconds >= 60 &&
      var.presigned_put_expiration_seconds <= 3600
    )

    error_message = "Presigned PUT expiration must be between 60 and 3600 seconds."
  }
}

variable "lambda_memory_size" {
  description = "Memory allocated to the WebDAV Lambda."
  type        = number
  default     = 256
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
  type        = string
  sensitive   = true
}

variable "webdav_password" {
  type        = string
  sensitive   = true

  validation {
    condition     = length(var.webdav_password) >= 5
    error_message = "The WebDAV password must contain at least 5 characters."
  }
}