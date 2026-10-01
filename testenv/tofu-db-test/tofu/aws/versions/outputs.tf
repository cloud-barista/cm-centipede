output "region" {
  value = var.aws_region
}

output "rds_versions" {
  value = local.version_report
}

# Keyed by the engine name RDS uses (postgres, not postgresql); the script maps
# it back to the transx-ex spelling.
output "suggested_versions" {
  description = "Candidate target versions per engine: the default plus its upgrade targets"
  value       = local.suggested
}

output "rds_instance_classes" {
  value = local.instance_class_report
}
