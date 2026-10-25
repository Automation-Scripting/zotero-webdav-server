############################################
# WebDAV Lambda package
############################################

data "archive_file" "webdav_lambda_zip" {
  type = "zip"

  source_file = "${path.module}/handler_webdav/bootstrap"
  output_path = "${path.module}/webdav-handler.zip"
}

############################################
# WebDAV Lambda logs
############################################

resource "aws_cloudwatch_log_group" "webdav_lambda" {
  name = "/aws/lambda/${local.project_full_name}-handler"

  retention_in_days = var.lambda_log_retention_days
}

############################################
# WebDAV Lambda
############################################

resource "aws_lambda_function" "webdav" {
  function_name = "${local.project_full_name}-handler"
  description   = "WebDAV server for Zotero files stored in Amazon S3."

  role = aws_iam_role.lambda_exec.arn

  runtime       = "provided.al2023"
  handler       = "bootstrap"
  architectures = ["arm64"]

  filename         = data.archive_file.webdav_lambda_zip.output_path
  source_code_hash = data.archive_file.webdav_lambda_zip.output_base64sha256

  memory_size = var.lambda_memory_size
  timeout     = var.lambda_timeout_seconds

  environment {
    variables = {
      LIBRARY_BUCKET = aws_s3_bucket.library.id

      WEBDAV_PREFIX       = "zotero"
      WEBDAV_USERNAME     = var.webdav_username
      WEBDAV_PASSWORD     = var.webdav_password
      WEBDAV_WORKER_TOKEN = var.webdav_worker_token

      PRESIGNED_GET_EXPIRATION = tostring(var.presigned_get_expiration_seconds)
      PRESIGNED_PUT_EXPIRATION = tostring(var.presigned_put_expiration_seconds)

      LOG_LEVEL = var.env == "prod" ? "INFO" : "DEBUG"
    }
  }

  depends_on = [
    aws_iam_role_policy_attachment.lambda_basic_execution,
    aws_iam_role_policy.lambda_webdav_access,
    aws_cloudwatch_log_group.webdav_lambda,
    aws_s3_object.webdav_collection_marker
  ]

  lifecycle {
    ignore_changes = [
      filename,
      source_code_hash
    ]
  }
}