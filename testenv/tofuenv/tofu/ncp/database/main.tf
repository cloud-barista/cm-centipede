# ---------------------------------------------------------------------------
# NCP Database
#   - MySQL / PostgreSQL / MongoDB : managed (Cloud DB)
#
#   Connecting to a managed DB from outside the VPC requires a public domain, which
#     can only be requested from the console - there is no API or provider argument
#     for it. Until it is issued, *_public_domain is null and there is no value to
#     put in the host position of a connection string.
#     After issuing it, run ./scripts/ncp-db-domain.sh to refresh state so the
#     outputs pick it up.
#
#   Every attribute of the managed DB resources is RequiresReplace and the provider
#     implements no Update. Changing a single variable re-creates the database, and
#     creation waits about 30 minutes.
# ---------------------------------------------------------------------------
locals {
  db_password = data.vault_kv_secret_v2.db.data["NCP_DB_PASSWORD"]

  # Ports match the AWS module so gendata and centipede configs stay CSP-agnostic.
  mysql_port    = 3306
  postgres_port = 5432
  # The managed MongoDB member_port defaults to 17017, but the accepted range is
  # 10000-65535, so 27017 is used. Provider validation passes; whether the API
  # accepts it is confirmed by the first apply.
  mongodb_port = 27017
}

# ---------------------------------------------------------------------------
# Look up what the network module created
# ---------------------------------------------------------------------------
data "ncloud_vpcs" "this" {
  name = "${var.ncp_name_prefix}-vpc"
}

data "ncloud_subnets" "public" {
  vpc_no = data.ncloud_vpcs.this.vpcs[0].vpc_no

  # ncloud_subnets has no name argument, so the name is matched through a filter.
  filter {
    name   = "name"
    values = ["${var.ncp_name_prefix}-subnet"]
  }
}

locals {
  vpc_no    = data.ncloud_vpcs.this.vpcs[0].vpc_no
  subnet_no = data.ncloud_subnets.public.subnets[0].subnet_no
}

# ---------------------------------------------------------------------------
# Fail-fast engine version check
#   Verifies at plan time that the requested version string exists in the catalog.
#   The filter is an exact match, so a partial version like "8.0" is caught here
#   instead of failing after a 30-minute apply.
# ---------------------------------------------------------------------------
data "ncloud_mysql_image_products" "pinned" {
  filter {
    name   = "engine_version_code"
    values = [var.ncp_mysql_version]
  }
}

data "ncloud_postgresql_image_products" "pinned" {
  filter {
    name   = "engine_version_code"
    values = [var.ncp_postgres_version]
  }
}

data "ncloud_mongodb_image_products" "pinned" {
  filter {
    name   = "engine_version_code"
    values = [var.ncp_mongodb_version]
  }
}

locals {
  mysql_version_ok    = length(data.ncloud_mysql_image_products.pinned.image_product_list) > 0
  postgres_version_ok = length(data.ncloud_postgresql_image_products.pinned.image_product_list) > 0
  mongodb_version_ok  = length(data.ncloud_mongodb_image_products.pinned.image_product_list) > 0
}

# ---------------------------------------------------------------------------
# Managed MySQL
#   With is_ha = false, supplying is_multi_zone / standby_master_subnet_no /
#   is_storage_encryption is itself an error, so they are omitted.
#   With is_backup = false, is_automatic_backup / backup_time must be omitted too.
#   There is no vpc_no argument; the provider derives it from subnet_no.
# ---------------------------------------------------------------------------
resource "ncloud_mysql" "this" {
  service_name        = "${var.ncp_name_prefix}-mysql"
  server_name_prefix  = "${var.ncp_name_prefix}-mysql"
  user_name           = var.ncp_db_username
  user_password       = local.db_password
  host_ip             = "%" # Allow any host so connections through the public domain work.
  database_name       = var.ncp_db_name
  subnet_no           = local.subnet_no
  is_ha               = false
  is_backup           = false
  port                = local.mysql_port
  engine_version_code = var.ncp_mysql_version

  lifecycle {
    precondition {
      condition     = local.mysql_version_ok
      error_message = "TF_VAR_ncp_mysql_version='${var.ncp_mysql_version}' is not in the supported list. It must be a full version string such as 8.0.36; run ./scripts/ncp-db-versions.sh mysql to list them."
    }
  }
}

# ---------------------------------------------------------------------------
# Managed PostgreSQL
#   ha / backup default to true in the provider, so they are set to false explicitly.
#   client_cidr is required and acts as an access control list.
#   data_storage_type keeps its default (the docs' data_storage_type_code is a typo).
# ---------------------------------------------------------------------------
resource "ncloud_postgresql" "this" {
  service_name        = "${var.ncp_name_prefix}-postgresql"
  server_name_prefix  = "${var.ncp_name_prefix}-pg"
  user_name           = var.ncp_db_username
  user_password       = local.db_password
  vpc_no              = local.vpc_no
  subnet_no           = local.subnet_no
  client_cidr         = var.allowed_cidr
  database_name       = var.ncp_db_name
  ha                  = false
  backup              = false
  port                = local.postgres_port
  engine_version_code = var.ncp_postgres_version

  lifecycle {
    precondition {
      condition     = local.postgres_version_ok
      error_message = "TF_VAR_ncp_postgres_version='${var.ncp_postgres_version}' is not in the supported list. It must be a full version string such as 14.22; run ./scripts/ncp-db-versions.sh postgresql to list them."
    }
  }
}

# ---------------------------------------------------------------------------
# Managed MongoDB (STAND_ALONE)
#   A replica set is not used: reaching one through a public domain requires editing
#   the client's hosts file and tolerating a brief outage.
#   Under STAND_ALONE, supplying member_server_count / arbiter_* / mongos_* /
#   config_* / shard_count is an error, so they are omitted.
#   compress_code is stated explicitly because, unlike the others, the provider
#   always forwards it to the API.
#   data_storage_type must be stated explicitly as well. It is Optional+Computed, and
#   provider 4.0.7 never fills it in during Create, so leaving it out makes the apply
#   fail with "provider still indicated an unknown value for
#   ncloud_mongodb.this.data_storage_type". A configured value is known at plan time and
#   avoids the bug - ncloud_mysql and ncloud_postgresql do not need this, the provider
#   fills the attribute for them.
#   The value cannot be SSD: NCP answers 400 / returnCode 5001451, "The Data Storage
#   Type (SSD) specification is not supported by the KR region, the G3 generation."
#   The codes the provider accepts are HDD, SSD, FB1, CB1, FB2 and CB2; CB2 is the
#   common block storage generation that KR / G3 serves.
# ---------------------------------------------------------------------------
resource "ncloud_mongodb" "this" {
  service_name        = "${var.ncp_name_prefix}-mongodb"
  server_name_prefix  = "${var.ncp_name_prefix}-mongo"
  user_name           = var.ncp_db_username
  user_password       = local.db_password
  vpc_no              = local.vpc_no
  subnet_no           = local.subnet_no
  cluster_type_code   = "STAND_ALONE"
  member_port         = local.mongodb_port
  compress_code       = "SNPP"
  data_storage_type   = "CB2"
  engine_version_code = var.ncp_mongodb_version

  lifecycle {
    precondition {
      condition     = local.mongodb_version_ok
      error_message = "TF_VAR_ncp_mongodb_version='${var.ncp_mongodb_version}' is not in the supported list. It must be a full version string such as 7.0.28; run ./scripts/ncp-db-versions.sh mongodb to list them."
    }
  }
}

# ---------------------------------------------------------------------------
# Pick the master / primary server of each managed DB
#   For MySQL and PostgreSQL, server_role is a code (M/H/S), but for MongoDB it is a
#   CodeName ("Member" and so on), so a role filter cannot be used there.
#   A STAND_ALONE MongoDB has exactly one server, so [0] is used instead.
# ---------------------------------------------------------------------------
locals {
  mysql_master = try(
    [for s in ncloud_mysql.this.mysql_server_list : s if s.server_role == "M"][0],
    ncloud_mysql.this.mysql_server_list[0]
  )
  postgres_primary = try(
    [for s in ncloud_postgresql.this.postgresql_server_list : s if s.server_role == "M"][0],
    ncloud_postgresql.this.postgresql_server_list[0]
  )
  mongodb_server = ncloud_mongodb.this.mongodb_server_list[0]

  # The public domain is null until it is issued from the console. An empty string
  # means the same thing, so both are normalized to null.
  mysql_public_domain_raw    = try(local.mysql_master.public_domain, null)
  postgres_public_domain_raw = try(local.postgres_primary.public_domain, null)
  mongodb_public_domain_raw  = try(local.mongodb_server.public_domain, null)

  mysql_public_domain    = (local.mysql_public_domain_raw == null || local.mysql_public_domain_raw == "") ? null : local.mysql_public_domain_raw
  postgres_public_domain = (local.postgres_public_domain_raw == null || local.postgres_public_domain_raw == "") ? null : local.postgres_public_domain_raw
  mongodb_public_domain  = (local.mongodb_public_domain_raw == null || local.mongodb_public_domain_raw == "") ? null : local.mongodb_public_domain_raw
}

# ---------------------------------------------------------------------------
# Inbound rules for the managed DB ACGs
#   NCP creates these ACGs automatically and the provider exposes them as read-only
#   attributes.
#   The provider docs warn that only ONE rule resource may target a given ACG, and
#   NCP fills each of these ACGs with its own rules, described as
#   "(automatically created, don't delete it) for the DB service itself."
#   The provider reads back every rule present in the ACG, so state holds those rules
#   too while the configuration below declares just one. Without ignore_changes each
#   apply plans an in-place update that DELETES NCP's own rules and can break the DB
#   service. Their ports (PostgreSQL uses 20021 next to 5432) are not exposed by any
#   data source, so re-declaring them here would mean hard-coding undocumented values.
#
#   ignore_changes is therefore used instead: Create only adds the rule below, and no
#   later apply computes an update, which leaves NCP's rules untouched.
#   Consequence: changing TF_VAR_allowed_cidr afterwards has no effect. Edit the rule
#   in the console, or run `tofu taint ncloud_access_control_group_rule.<name>` to
#   have it recreated.
# ---------------------------------------------------------------------------
resource "ncloud_access_control_group_rule" "mysql" {
  access_control_group_no = ncloud_mysql.this.access_control_group_no_list[0]

  inbound {
    protocol    = "TCP"
    ip_block    = var.allowed_cidr
    port_range  = tostring(local.mysql_port)
    description = "MySQL from outside"
  }

  lifecycle {
    ignore_changes = [inbound, outbound]
  }
}

resource "ncloud_access_control_group_rule" "postgresql" {
  access_control_group_no = ncloud_postgresql.this.access_control_group_no_list[0]

  inbound {
    protocol    = "TCP"
    ip_block    = var.allowed_cidr
    port_range  = tostring(local.postgres_port)
    description = "PostgreSQL from outside"
  }

  lifecycle {
    ignore_changes = [inbound, outbound]
  }
}

resource "ncloud_access_control_group_rule" "mongodb" {
  access_control_group_no = ncloud_mongodb.this.access_control_group_no_list[0]

  inbound {
    protocol    = "TCP"
    ip_block    = var.allowed_cidr
    port_range  = tostring(local.mongodb_port)
    description = "MongoDB from outside"
  }

  lifecycle {
    ignore_changes = [inbound, outbound]
  }
}
