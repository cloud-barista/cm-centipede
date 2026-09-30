variable "aws_region" {
  description = "AWS region"
  type        = string
  default     = "ap-northeast-2"
}

variable "aws_bucket_name" {
  description = "S3 bucket name (must be globally unique)"
  type        = string
}

variable "aws_name_prefix" {
  description = "Resource name prefix, short for centipede tofu. The bucket name does not use it (it is set in full); it names the environment this bucket belongs to"
  type        = string
  default     = "cptf"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,19}$", var.aws_name_prefix))
    error_message = "Prefix must be 2-20 characters of lowercase letters, digits and hyphens, starting with a lowercase letter."
  }
}
