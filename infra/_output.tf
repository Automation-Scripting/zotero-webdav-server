output "webdav_api_url" {
  value = aws_apigatewayv2_stage.default.invoke_url
}

output "zotero_webdav_url" {
  value = "${trimsuffix(aws_apigatewayv2_stage.default.invoke_url, "/")}/zotero/"
}

output "library_bucket_name" {
  value = aws_s3_bucket.library.id
}

output "webdav_lambda_name" {
  value = aws_lambda_function.webdav.function_name
}

output "github_lambda_deploy_role_arn" {
  value = aws_iam_role.github_webdav_deployer.arn
}