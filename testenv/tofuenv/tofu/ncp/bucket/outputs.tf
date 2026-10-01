output "bucket_name" {
  description = "Name of the created bucket (gendata uses it as the upload target)"
  value       = ncloud_objectstorage_bucket.this.bucket_name
}

output "bucket_region" {
  description = "NCP region code (gendata converts it into the S3 endpoint and signing region)"
  value       = var.ncp_region
}

output "bucket_creation_date" {
  value = ncloud_objectstorage_bucket.this.creation_date
}

output "s3_endpoint" {
  description = "S3-compatible API endpoint"
  value       = local.os_endpoint
}

output "s3_signing_region" {
  description = "S3 signing region (differs from the provider region)"
  value       = local.os_signing_region
}

output "s3cmd_hint" {
  description = "Example command for checking the bucket with the AWS CLI"
  value       = "aws --endpoint-url https://${local.os_endpoint} s3 ls s3://${ncloud_objectstorage_bucket.this.bucket_name}"
}

output "name_prefix" {
  description = "Name prefix, and the tofu workspace this environment lives in"
  value       = var.ncp_name_prefix
}
