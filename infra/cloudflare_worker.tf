locals {
  worker_script_path = fileexists("${path.module}/../worker/worker.mjs") ? "${path.module}/../worker/worker.mjs" : "${path.module}/worker.mjs"
}

resource "cloudflare_workers_script" "webdav" {
  account_id         = var.cloudflare_account_id
  script_name        = var.cloudflare_worker_name
  content_file       = local.worker_script_path
  content_sha256     = filesha256(local.worker_script_path)
  main_module        = "worker.mjs"
  compatibility_date = "2026-07-01"

  bindings = [
    {
      name = "API_ORIGIN"
      type = "plain_text"
      text = trimsuffix(aws_apigatewayv2_stage.default.invoke_url, "/")
    },
    {
      name = "S3_HOST"
      type = "plain_text"
      text = "${aws_s3_bucket.library.id}.s3.${var.aws_region}.amazonaws.com"
    },
    {
      name = "WEBDAV_PREFIX"
      type = "plain_text"
      text = "zotero"
    },
    {
      name = "UPLOAD_MODE"
      type = "plain_text"
      text = var.cloudflare_upload_mode
    },
    {
      name = "MAX_UPLOAD_BYTES"
      type = "plain_text"
      text = tostring(var.cloudflare_max_upload_bytes)
    },
    {
      name = "WORKER_TOKEN"
      type = "secret_text"
      text = var.webdav_worker_token
    }
  ]

  observability = {
    enabled = false
  }
}

resource "cloudflare_workers_script_subdomain" "webdav" {
  account_id       = var.cloudflare_account_id
  script_name      = cloudflare_workers_script.webdav.script_name
  enabled          = true
  previews_enabled = false
}

