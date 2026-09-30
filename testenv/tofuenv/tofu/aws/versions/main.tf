# ---------------------------------------------------------------------------
# AWS catalog lookup module - data sources only, it creates nothing.
# ---------------------------------------------------------------------------
#   Purpose: find the exact values to put into .env.
#     - TF_VAR_aws_{mysql,mariadb,postgres}_version : RDS engine versions
#     - TF_VAR_aws_db_instance_class                : RDS instance class
#     - TF_VAR_aws_instance_type                    : EC2 instance type
#
#   Unlike NCP, RDS accepts a partial engine_version such as "8.0" and resolves it to
#     the current minor release, which the provider tracks separately in
#     engine_version_actual. A partial version therefore does NOT schedule a
#     replacement on every plan, so both "8.0" and "8.0.42" are safe here.
#
#   Limitation: the AWS provider has no data source that lists every RDS engine
#     version - aws_rds_engine_version returns exactly one. What is reported below is
#     the default version, the latest version, and the upgrade targets reachable from
#     the default, which together cover what is worth putting in .env. For the
#     exhaustive list use the AWS CLI:
#       aws rds describe-db-engine-versions --engine mysql --region <region> \
#         --query 'DBEngineVersions[].EngineVersion'
#
#   Run: ./scripts/aws-db-versions.sh
# ---------------------------------------------------------------------------

# --- RDS engine versions ---------------------------------------------------
# default_only reports what RDS picks when no version is given; latest reports the
# newest available one.
data "aws_rds_engine_version" "mysql_default" {
  engine       = "mysql"
  default_only = true
}

data "aws_rds_engine_version" "mysql_latest" {
  engine = "mysql"
  latest = true
}

data "aws_rds_engine_version" "mariadb_default" {
  engine       = "mariadb"
  default_only = true
}

data "aws_rds_engine_version" "mariadb_latest" {
  engine = "mariadb"
  latest = true
}

data "aws_rds_engine_version" "postgres_default" {
  engine       = "postgres"
  default_only = true
}

data "aws_rds_engine_version" "postgres_latest" {
  engine = "postgres"
  latest = true
}

# --- RDS instance classes --------------------------------------------------
# aws_rds_orderable_db_instance returns ONE class: the first entry of
# preferred_instance_classes that the engine actually offers in this region. It is a
# check of whether TF_VAR_aws_db_instance_class is orderable, with fallbacks listed
# after it. The storage bounds also tell you what TF_VAR_aws_db_allocated_storage allows.
locals {
  preferred_db_classes = [
    var.aws_db_instance_class,
    "db.t4g.micro",
    "db.t3.small",
    "db.t4g.small",
    "db.t3.medium",
  ]

  # EC2 instance type families worth listing. Widen this if you need other families.
  ec2_type_patterns = ["t3.*", "t4g.*"]
}

data "aws_rds_orderable_db_instance" "mysql" {
  engine                     = "mysql"
  engine_version             = data.aws_rds_engine_version.mysql_default.version_actual
  preferred_instance_classes = local.preferred_db_classes
}

data "aws_rds_orderable_db_instance" "mariadb" {
  engine                     = "mariadb"
  engine_version             = data.aws_rds_engine_version.mariadb_default.version_actual
  preferred_instance_classes = local.preferred_db_classes
}

data "aws_rds_orderable_db_instance" "postgres" {
  engine                     = "postgres"
  engine_version             = data.aws_rds_engine_version.postgres_default.version_actual
  preferred_instance_classes = local.preferred_db_classes
}

# --- EC2 instance types available in this region ----------------------------
data "aws_ec2_instance_type_offerings" "region" {
  location_type = "region"

  filter {
    name   = "instance-type"
    values = local.ec2_type_patterns
  }

  filter {
    name   = "location"
    values = [var.aws_region]
  }
}

# --- Ubuntu AMI -------------------------------------------------------------
# The same filters as tofu/aws/vm and tofu/aws/database, so this reports the AMI those
# modules will actually pick. There is no TF_VAR for it; it is resolved at apply time.
data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"] # Canonical

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*"]
  }
  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

# --- Shape the results into printable maps ---------------------------------
# aws-db-versions.sh reads these field names explicitly, so it controls the print order.
locals {
  engine_versions = {
    mysql    = { default = data.aws_rds_engine_version.mysql_default, latest = data.aws_rds_engine_version.mysql_latest }
    mariadb  = { default = data.aws_rds_engine_version.mariadb_default, latest = data.aws_rds_engine_version.mariadb_latest }
    postgres = { default = data.aws_rds_engine_version.postgres_default, latest = data.aws_rds_engine_version.postgres_latest }
  }

  version_report = {
    for engine, v in local.engine_versions : engine => {
      default_version        = v.default.version_actual
      latest_version         = v.latest.version_actual
      minor_upgrade_targets  = length(v.default.valid_minor_targets) > 0 ? join(", ", sort(tolist(v.default.valid_minor_targets))) : "(none)"
      major_upgrade_targets  = length(v.default.valid_major_targets) > 0 ? join(", ", sort(tolist(v.default.valid_major_targets))) : "(none)"
      parameter_group_family = v.default.parameter_group_family
    }
  }

  orderable = {
    mysql    = data.aws_rds_orderable_db_instance.mysql
    mariadb  = data.aws_rds_orderable_db_instance.mariadb
    postgres = data.aws_rds_orderable_db_instance.postgres
  }

  instance_class_report = {
    for engine, o in local.orderable :
    engine => format("%s (engine %s, storage %s, %d-%d GB)",
      o.instance_class,
      o.engine_version,
      o.storage_type,
      o.min_storage_size,
      o.max_storage_size,
    )
  }
}
