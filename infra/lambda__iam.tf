resource "aws_iam_role" "lambda_exec" {
  name = "${local.project_full_name}-lambda-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"

    Statement = [{
      Effect = "Allow"

      Action = "sts:AssumeRole"

      Principal = {
        Service = "lambda.amazonaws.com"
      }
    }]
  })
}

resource "aws_iam_role_policy_attachment" "lambda_basic_execution" {
  role       = aws_iam_role.lambda_exec.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "aws_iam_policy_document" "lambda_webdav_access" {

  statement {

    actions = [
      "s3:ListBucket",
      "s3:GetBucketLocation"
    ]

    resources = [
      aws_s3_bucket.library.arn
    ]
  }

  statement {

    actions = [
      "s3:GetObject",
      "s3:GetObjectAttributes",
      "s3:GetObjectVersion",
      "s3:PutObject",
      "s3:DeleteObject"
    ]

    resources = [
      "${aws_s3_bucket.library.arn}/zotero/*"
    ]
  }
}

resource "aws_iam_role_policy" "lambda_webdav_access" {
  name = "${local.project_full_name}-webdav-access"

  role   = aws_iam_role.lambda_exec.id
  policy = data.aws_iam_policy_document.lambda_webdav_access.json
}