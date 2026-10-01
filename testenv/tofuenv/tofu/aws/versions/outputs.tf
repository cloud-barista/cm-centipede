# RDS engine versions per engine.
#   default_version / latest_version are full version strings; either the full string
#   or its "8.0" style prefix is a valid TF_VAR_aws_*_version.
output "rds_versions" {
  description = "Engine -> {default_version, latest_version, upgrade targets, parameter group family}"
  value       = local.version_report
}

# Engine -> the first orderable class out of preferred_db_classes, with storage bounds.
output "rds_instance_classes" {
  description = "Engine -> orderable RDS instance class (TF_VAR_aws_db_instance_class)"
  value       = local.instance_class_report
}

# EC2 instance types offered in this region (TF_VAR_aws_instance_type).
output "ec2_instance_types" {
  description = "EC2 instance types available in the region, limited to the listed families"
  value       = sort(data.aws_ec2_instance_type_offerings.region.instance_types)
}

# The Ubuntu AMI that tofu/aws/vm and tofu/aws/database resolve at apply time.
output "ubuntu_ami" {
  description = "Ubuntu 22.04 AMI the aws modules will use"
  value = {
    ami_id        = data.aws_ami.ubuntu.id
    ami_name      = data.aws_ami.ubuntu.name
    architecture  = data.aws_ami.ubuntu.architecture
    creation_date = data.aws_ami.ubuntu.creation_date
  }
}

output "region" {
  description = "Region these results were queried in"
  value       = var.aws_region
}
