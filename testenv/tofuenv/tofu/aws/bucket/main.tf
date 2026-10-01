# S3 bucket used as the object storage migration test target.
resource "aws_s3_bucket" "this" {
  bucket        = var.aws_bucket_name
  force_destroy = true # Test convenience: allow destroy even when objects remain.

  tags = {
    Project   = "cm-centipede"
    ManagedBy = "opentofu"
    Purpose   = "migration-test"
  }
}
