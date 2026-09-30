variable "ncp_region" {
  description = "NCP region code"
  type        = string
  default     = "KR"
}

variable "ncp_bucket_name" {
  description = "Object Storage bucket name: 3-63 chars of lowercase letters, digits, dots and hyphens; must start and end with a letter or digit; no consecutive dots"
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", var.ncp_bucket_name)) && !can(regex("\\.\\.", var.ncp_bucket_name))
    error_message = "Bucket name must be 3-63 characters of lowercase letters, digits, dots and hyphens, start and end with a letter or digit, and contain no consecutive dots."
  }
}

variable "ncp_name_prefix" {
  description = "Resource name prefix, short for centipede tofu. The bucket name does not use it (it is set in full); it names the environment this bucket belongs to"
  type        = string
  default     = "cptf"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,9}$", var.ncp_name_prefix))
    error_message = "Prefix must be 2-10 characters of lowercase letters, digits and hyphens, starting with a lowercase letter (to stay within the 15-character MongoDB name limit)."
  }
}
