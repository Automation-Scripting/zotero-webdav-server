locals {
  project_full_name = "${var.project_name}-${var.env}"
  github_sub = "repo:Automation-Scripting@250155963/webdav-server@1306748416:ref:refs/heads/${var.env}"
}

data "aws_caller_identity" "me" {}
