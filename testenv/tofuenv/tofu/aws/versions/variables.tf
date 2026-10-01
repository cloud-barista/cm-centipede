variable "aws_region" {
  description = "AWS region"
  type        = string
  default     = "ap-northeast-2"
}

variable "aws_db_instance_class" {
  description = "RDS instance class to check for orderability (TF_VAR_aws_db_instance_class)"
  type        = string
  default     = "db.t3.micro"
}
