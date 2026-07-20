resource "aws_cloudwatch_log_group" "webdav_api" {
  name = "/aws/apigateway/${local.project_full_name}"

  retention_in_days = var.api_log_retention_days
}

resource "aws_apigatewayv2_stage" "default" {

  api_id = aws_apigatewayv2_api.webdav.id

  name = "$default"

  auto_deploy = true

  default_route_settings {

    detailed_metrics_enabled = true

    throttling_burst_limit = var.api_throttling_burst_limit
    throttling_rate_limit  = var.api_throttling_rate_limit
  }

  access_log_settings {

    destination_arn = aws_cloudwatch_log_group.webdav_api.arn

    format = jsonencode({

      requestId = "$context.requestId"

      httpMethod = "$context.httpMethod"

      path = "$context.path"

      status = "$context.status"
    })
  }
}