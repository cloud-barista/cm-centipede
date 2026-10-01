locals {
  db_password = data.vault_kv_secret_v2.db.data["AWS_DB_PASSWORD"]

  want_parameter_group = var.aws_secure_transport == "off"
}

# ---------------------------------------------------------------------------
# Network (uses the default VPC)
# ---------------------------------------------------------------------------
data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
}

resource "aws_db_subnet_group" "this" {
  name       = "${var.aws_name_prefix}-db-subnet"
  subnet_ids = data.aws_subnets.default.ids

  tags = {
    Project   = "cm-centipede"
    ManagedBy = "opentofu"
  }
}

# ---------------------------------------------------------------------------
# Security groups
# ---------------------------------------------------------------------------
resource "aws_security_group" "rds" {
  name        = "${var.aws_name_prefix}-rds-sg"
  description = "cm-centipede RDS (mysql/mariadb/postgres)"
  vpc_id      = data.aws_vpc.default.id

  ingress {
    description = "MySQL/MariaDB"
    from_port   = 3306
    to_port     = 3306
    protocol    = "tcp"
    cidr_blocks = [var.allowed_cidr]
  }
  ingress {
    description = "PostgreSQL"
    from_port   = 5432
    to_port     = 5432
    protocol    = "tcp"
    cidr_blocks = [var.allowed_cidr]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Project   = "cm-centipede"
    ManagedBy = "opentofu"
  }
}

# ---------------------------------------------------------------------------
# Parameter groups that allow plaintext connections
#
# Only created when TF_VAR_aws_secure_transport=off. One per engine, because a
# parameter group belongs to exactly one engine family and the parameter is
# spelled differently per engine.
#
# parameter_group_family is read from the provider rather than built from the
# version string: the rule differs per engine (mysql8.0, mariadb11.8,
# postgres16) and a version may be given as a prefix or in full. These data
# sources are gated by the same count, so the default path makes no extra API
# call and keeps behaving exactly as before.
#
# name_prefix, because family and name both force replacement and a group still
# attached to a live instance cannot be destroyed first.
#
# ⚠ The group is attached at creation time, so the instance boots with it and no
#   reboot is ever needed. Attaching one to an instance that is already running
#   would leave postgres pending-reboot, which is why flipping this value means
#   destroying and re-provisioning the module rather than re-applying it.
# ---------------------------------------------------------------------------
data "aws_rds_engine_version" "mysql" {
  count   = local.want_parameter_group ? 1 : 0
  engine  = "mysql"
  version = var.aws_mysql_version
}

resource "aws_db_parameter_group" "mysql" {
  count = local.want_parameter_group ? 1 : 0

  name_prefix = "${var.aws_name_prefix}-mysql-"
  family      = data.aws_rds_engine_version.mysql[0].parameter_group_family
  description = "cm-centipede tofuenv: allow plaintext client connections"

  # Dynamic in MySQL, so it takes effect without a reboot.
  parameter {
    name         = "require_secure_transport"
    value        = "0"
    apply_method = "immediate"
  }

  lifecycle { create_before_destroy = true }

  tags = { Project = "cm-centipede", ManagedBy = "opentofu", Engine = "mysql" }
}

data "aws_rds_engine_version" "mariadb" {
  count   = local.want_parameter_group ? 1 : 0
  engine  = "mariadb"
  version = var.aws_mariadb_version
}

resource "aws_db_parameter_group" "mariadb" {
  count = local.want_parameter_group ? 1 : 0

  name_prefix = "${var.aws_name_prefix}-mariadb-"
  family      = data.aws_rds_engine_version.mariadb[0].parameter_group_family
  description = "cm-centipede tofuenv: allow plaintext client connections"

  parameter {
    name         = "require_secure_transport"
    value        = "0"
    apply_method = "immediate"
  }

  lifecycle { create_before_destroy = true }

  tags = { Project = "cm-centipede", ManagedBy = "opentofu", Engine = "mariadb" }
}

data "aws_rds_engine_version" "postgres" {
  count   = local.want_parameter_group ? 1 : 0
  engine  = "postgres"
  version = var.aws_postgres_version
}

resource "aws_db_parameter_group" "postgres" {
  count = local.want_parameter_group ? 1 : 0

  name_prefix = "${var.aws_name_prefix}-postgres-"
  family      = data.aws_rds_engine_version.postgres[0].parameter_group_family
  description = "cm-centipede tofuenv: allow plaintext client connections"

  # rds.force_ssl is static, so it only takes effect on boot. The group is
  # attached at creation time, so that boot is the instance's first one.
  parameter {
    name         = "rds.force_ssl"
    value        = "0"
    apply_method = "pending-reboot"
  }

  lifecycle { create_before_destroy = true }

  tags = { Project = "cm-centipede", ManagedBy = "opentofu", Engine = "postgres" }
}

# ---------------------------------------------------------------------------
# RDS - MySQL / MariaDB / PostgreSQL
#   engine_version comes from .env (TF_VAR_aws_*_version); the defaults match testenv.
#   Requesting a version the region does not offer fails at apply time; pick another
#   version from the supported list in .env.
#   (An in-place downgrade is not possible, so a version change means re-provisioning
#   the database module.)
#   TF_VAR_aws_secure_transport is fixed at creation time for the same reason: a
#   parameter group attached later would leave postgres pending-reboot.
# ---------------------------------------------------------------------------
resource "aws_db_instance" "mysql" {
  identifier             = "${var.aws_name_prefix}-mysql"
  engine                 = "mysql"
  engine_version         = var.aws_mysql_version
  instance_class         = var.aws_db_instance_class
  allocated_storage      = var.aws_db_allocated_storage
  db_name                = var.aws_db_name
  username               = var.aws_db_username
  password               = local.db_password
  port                   = 3306
  publicly_accessible    = true
  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.rds.id]
  parameter_group_name   = local.want_parameter_group ? aws_db_parameter_group.mysql[0].name : null
  skip_final_snapshot    = true
  apply_immediately      = true

  tags = { Project = "cm-centipede", ManagedBy = "opentofu", Engine = "mysql" }
}

resource "aws_db_instance" "mariadb" {
  identifier             = "${var.aws_name_prefix}-mariadb"
  engine                 = "mariadb"
  engine_version         = var.aws_mariadb_version
  instance_class         = var.aws_db_instance_class
  allocated_storage      = var.aws_db_allocated_storage
  db_name                = var.aws_db_name
  username               = var.aws_db_username
  password               = local.db_password
  port                   = 3306
  publicly_accessible    = true
  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.rds.id]
  parameter_group_name   = local.want_parameter_group ? aws_db_parameter_group.mariadb[0].name : null
  skip_final_snapshot    = true
  apply_immediately      = true

  tags = { Project = "cm-centipede", ManagedBy = "opentofu", Engine = "mariadb" }
}

resource "aws_db_instance" "postgres" {
  identifier             = "${var.aws_name_prefix}-postgres"
  engine                 = "postgres"
  engine_version         = var.aws_postgres_version
  instance_class         = var.aws_db_instance_class
  allocated_storage      = var.aws_db_allocated_storage
  db_name                = var.aws_db_name
  username               = var.aws_db_username
  password               = local.db_password
  port                   = 5432
  publicly_accessible    = true
  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.rds.id]
  parameter_group_name   = local.want_parameter_group ? aws_db_parameter_group.postgres[0].name : null
  skip_final_snapshot    = true
  apply_immediately      = true

  tags = { Project = "cm-centipede", ManagedBy = "opentofu", Engine = "postgres" }
}
