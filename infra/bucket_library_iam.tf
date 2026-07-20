data "aws_iam_policy_document" "library_bucket_policy" {
  statement {
    sid    = "DenyInsecureTransport"
    effect = "Deny"

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    actions = [
      "s3:*"
    ]

    resources = [
      aws_s3_bucket.library.arn,
      "${aws_s3_bucket.library.arn}/*"
    ]

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "library" {
  bucket = aws_s3_bucket.library.id
  policy = data.aws_iam_policy_document.library_bucket_policy.json

  depends_on = [
    aws_s3_bucket_public_access_block.library
  ]
}