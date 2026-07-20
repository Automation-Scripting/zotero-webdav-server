resource "aws_apigatewayv2_api" "webdav" {
  name          = "${local.project_full_name}-api"
  protocol_type = "HTTP"
}

resource "aws_apigatewayv2_integration" "webdav_lambda" {

  api_id = aws_apigatewayv2_api.webdav.id

  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.webdav.invoke_arn
  integration_method     = "POST"
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "default" {

  api_id = aws_apigatewayv2_api.webdav.id

  route_key = "$default"

  target = "integrations/${aws_apigatewayv2_integration.webdav_lambda.id}"
}

resource "aws_lambda_permission" "allow_api_gateway" {

  statement_id = "AllowExecutionFromAPIGateway"

  action = "lambda:InvokeFunction"

  function_name = aws_lambda_function.webdav.function_name

  principal = "apigateway.amazonaws.com"

  source_arn = "${aws_apigatewayv2_api.webdav.execution_arn}/*/*"
}