resource "aws_s3_bucket" "library" {
  bucket = "${local.project_full_name}-library"
}

resource "aws_s3_bucket_public_access_block" "library" {
  bucket = aws_s3_bucket.library.id

  block_public_acls       = true
  ignore_public_acls      = true
  block_public_policy     = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "library" {
  bucket = aws_s3_bucket.library.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_versioning" "library" {
  bucket = aws_s3_bucket.library.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "library" {
  bucket = aws_s3_bucket.library.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_object" "webdav_collection_marker" {
  bucket       = aws_s3_bucket.library.id
  key          = "zotero/"
  content      = ""
  content_type = "application/x-directory"
}