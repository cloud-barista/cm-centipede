output "bucket_name" {
  description = "Name of the created S3 bucket"
  value       = aws_s3_bucket.this.id
}

output "bucket_arn" {
  value = aws_s3_bucket.this.arn
}

output "bucket_region" {
  value = aws_s3_bucket.this.region
}

output "bucket_domain_name" {
  description = "Bucket regional domain name (endpoint)"
  value       = aws_s3_bucket.this.bucket_regional_domain_name
}

output "name_prefix" {
  description = "Name prefix, and the tofu workspace this environment lives in"
  value       = var.aws_name_prefix
}
