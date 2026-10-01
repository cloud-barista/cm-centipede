# ---------------------------------------------------------------------------
# One managed RDS instance = one column of the version matrix.
#
# The matrix applies this module once per target version, runs every source
# version against it, then destroys it. Names carry the engine and the version
# so a column left behind with --keep-instance cannot collide with the next one.
# ---------------------------------------------------------------------------
locals {
  db_password = data.vault_kv_secret_v2.db.data["AWS_DB_PASSWORD"]

  # Hyphens only: an RDS identifier and a parameter group name reject dots.
  version_slug = replace(var.engine_version, ".", "-")
  base_name    = "${var.name_prefix}-${var.engine}-${local.version_slug}"

  port = var.engine == "postgres" ? 5432 : 3306

  # The parameter that lets a plaintext client connect. It is spelled
  # differently per engine, and PostgreSQL's is static, so it is applied on
  # reboot rather than immediately. Attaching the group at creation time means
  # the instance boots with it either way and no reboot is ever needed.
  insecure_param = {
    mysql    = { name = "require_secure_transport", value = "0", apply = "immediate" }
    mariadb  = { name = "require_secure_transport", value = "0", apply = "immediate" }
    postgres = { name = "rds.force_ssl", value = "0", apply = "pending-reboot" }
  }

  want_parameter_group = var.secure_transport == "off"
}

# ---------------------------------------------------------------------------
# Network - the default VPC is used as it comes.
#   No vNet or subnet has to be created: RDS only needs a subnet group over the
#   subnets that already exist.
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
  name       = "${local.base_name}-subnet"
  subnet_ids = data.aws_subnets.default.ids

  tags = {
    Name      = "${local.base_name}-subnet"
    Project   = "cm-centipede"
    ManagedBy = "opentofu"
    Matrix    = "db-migration-tofu-ver-matrix"
  }
}

resource "aws_security_group" "this" {
  name        = "${local.base_name}-sg"
  description = "cm-centipede version matrix target (${var.engine} ${var.engine_version})"
  vpc_id      = data.aws_vpc.default.id

  ingress {
    description = "engine port"
    from_port   = local.port
    to_port     = local.port
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
    Name      = "${local.base_name}-sg"
    Project   = "cm-centipede"
    ManagedBy = "opentofu"
    Matrix    = "db-migration-tofu-ver-matrix"
  }
}

# ---------------------------------------------------------------------------
# Version check, and the parameter group family that goes with it.
#
#   Asking for a version the region does not offer fails here, at plan time,
#   before anything is created. That is the whole pre-flight: RDS is handed the
#   string as given, and a version it does not offer is an error, not a
#   substitution.
#
#   parameter_group_family is read rather than built from the version string:
#   the rule differs per engine (mysql8.0, mariadb11.8, postgres16) and a
#   version may be given as a prefix or in full.
# ---------------------------------------------------------------------------
data "aws_rds_engine_version" "this" {
  engine  = var.engine
  version = var.engine_version
}

resource "aws_db_parameter_group" "this" {
  count = local.want_parameter_group ? 1 : 0

  # name_prefix, because family and name both force replacement and a group
  # still attached to a live instance cannot be destroyed first.
  name_prefix = "${local.base_name}-"
  family      = data.aws_rds_engine_version.this.parameter_group_family
  description = "cm-centipede version matrix: allow plaintext client connections"

  parameter {
    name         = local.insecure_param[var.engine].name
    value        = local.insecure_param[var.engine].value
    apply_method = local.insecure_param[var.engine].apply
  }

  lifecycle {
    create_before_destroy = true
  }

  tags = {
    Project   = "cm-centipede"
    ManagedBy = "opentofu"
    Matrix    = "db-migration-tofu-ver-matrix"
  }
}

resource "aws_db_instance" "this" {
  identifier     = local.base_name
  engine         = var.engine
  engine_version = var.engine_version
  instance_class = var.db_instance_class

  allocated_storage = var.db_allocated_storage
  db_name           = var.db_name
  username          = var.db_username
  password          = local.db_password
  port              = local.port

  publicly_accessible    = true
  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.this.id]
  parameter_group_name   = local.want_parameter_group ? aws_db_parameter_group.this[0].name : null

  # A matrix instance is disposable: it lives for one column and is destroyed
  # with it, so a final snapshot would only cost storage and time.
  skip_final_snapshot = true
  apply_immediately   = true

  tags = {
    Name      = local.base_name
    Project   = "cm-centipede"
    ManagedBy = "opentofu"
    Matrix    = "db-migration-tofu-ver-matrix"
    Engine    = var.engine
    Version   = var.engine_version
  }
}
