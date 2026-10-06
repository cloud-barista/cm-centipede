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

  # Each engine exists only when var.ncp_db_engines names it, so provision.sh /
  # deprovision.sh --engine can add or remove one without touching the others.
  want_mysql    = contains(var.ncp_db_engines, "mysql")
  want_postgres = contains(var.ncp_db_engines, "postgres")
  want_mongodb  = contains(var.ncp_db_engines, "mongodb")
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
#   instead of failing after a 30-minute apply. An engine that is not wanted is
#   not looked up, and its precondition is never evaluated.
# ---------------------------------------------------------------------------
data "ncloud_mysql_image_products" "pinned" {
  count = local.want_mysql ? 1 : 0

  filter {
    name   = "engine_version_code"
    values = [var.ncp_mysql_version]
  }
}

data "ncloud_postgresql_image_products" "pinned" {
  count = local.want_postgres ? 1 : 0

  filter {
    name   = "engine_version_code"
    values = [var.ncp_postgres_version]
  }
}

data "ncloud_mongodb_image_products" "pinned" {
  count = local.want_mongodb ? 1 : 0

  filter {
    name   = "engine_version_code"
    values = [var.ncp_mongodb_version]
  }
}

locals {
  mysql_version_ok    = try(length(data.ncloud_mysql_image_products.pinned[0].image_product_list) > 0, false)
  postgres_version_ok = try(length(data.ncloud_postgresql_image_products.pinned[0].image_product_list) > 0, false)
  mongodb_version_ok  = try(length(data.ncloud_mongodb_image_products.pinned[0].image_product_list) > 0, false)

  # The images each pinned version comes as. A TF_VAR_ncp_*_image_product_code
  # must be one of them; checking at plan time beats a refusal 30 minutes in.
  mysql_images    = try([for i in data.ncloud_mysql_image_products.pinned[0].image_product_list : i.product_code], [])
  postgres_images = try([for i in data.ncloud_postgresql_image_products.pinned[0].image_product_list : i.product_code], [])
  mongodb_images  = try([for i in data.ncloud_mongodb_image_products.pinned[0].image_product_list : i.product_code], [])

  mysql_image_ok    = var.ncp_mysql_image_product_code == "" || contains(local.mysql_images, var.ncp_mysql_image_product_code)
  postgres_image_ok = var.ncp_postgres_image_product_code == "" || contains(local.postgres_images, var.ncp_postgres_image_product_code)
  mongodb_image_ok  = var.ncp_mongodb_image_product_code == "" || contains(local.mongodb_images, var.ncp_mongodb_image_product_code)

  # The pinned MongoDB image's generation (G2 / G3), "" when none is pinned.
  mongodb_image_generation = try([
    for i in data.ncloud_mongodb_image_products.pinned[0].image_product_list :
    i.generation_code if i.product_code == var.ncp_mongodb_image_product_code
  ][0], "")
}

# ---------------------------------------------------------------------------
# Managed MySQL
#   With is_ha = false, supplying is_multi_zone / standby_master_subnet_no /
#   is_storage_encryption is itself an error, so they are omitted.
#   With is_backup = false, is_automatic_backup / backup_time must be omitted too.
#   There is no vpc_no argument; the provider derives it from subnet_no.
# ---------------------------------------------------------------------------
resource "ncloud_mysql" "this" {
  count = local.want_mysql ? 1 : 0

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
  image_product_code  = var.ncp_mysql_image_product_code == "" ? null : var.ncp_mysql_image_product_code
  product_code        = var.ncp_mysql_product_code == "" ? null : var.ncp_mysql_product_code

  lifecycle {
    precondition {
      condition     = local.mysql_version_ok
      error_message = "TF_VAR_ncp_mysql_version='${var.ncp_mysql_version}' is not in the supported list. It must be a full version string such as 8.0.36; run ./scripts/ncp-db-versions.sh mysql to list them."
    }
    precondition {
      condition     = local.mysql_image_ok
      error_message = "TF_VAR_ncp_mysql_image_product_code='${var.ncp_mysql_image_product_code}' is not an image of MySQL ${var.ncp_mysql_version}. Run ./scripts/ncp-db-versions.sh mysql and copy an [image ...] code."
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
  count = local.want_postgres ? 1 : 0

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
  image_product_code  = var.ncp_postgres_image_product_code == "" ? null : var.ncp_postgres_image_product_code
  product_code        = var.ncp_postgres_product_code == "" ? null : var.ncp_postgres_product_code

  lifecycle {
    precondition {
      condition     = local.postgres_version_ok
      error_message = "TF_VAR_ncp_postgres_version='${var.ncp_postgres_version}' is not in the supported list. It must be a full version string such as 14.22; run ./scripts/ncp-db-versions.sh postgresql to list them."
    }
    precondition {
      condition     = local.postgres_image_ok
      error_message = "TF_VAR_ncp_postgres_image_product_code='${var.ncp_postgres_image_product_code}' is not an image of PostgreSQL ${var.ncp_postgres_version}. Run ./scripts/ncp-db-versions.sh postgresql and copy an [image ...] code."
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
#   The storage type follows the server generation, and the other way round is
#   refused too: a G2 spec with CB2 answers 5001451, "The Data Storage Type (CB2)
#   specification is not supported by the KR region, the G2 generation." The
#   generation is the product code's last field (...G002 / ...G003) or the pinned
#   image's generation_code, so a G2 spec or a G2 image gets SSD and everything
#   else - G3, or nothing pinned, which NCP builds as G3 - keeps CB2.
# ---------------------------------------------------------------------------
locals {
  mongodb_g2 = (
    can(regex("\\.G002$", var.ncp_mongodb_product_code)) ||
    contains(["G2", "G002"], upper(local.mongodb_image_generation))
  )
  mongodb_data_storage_type = local.mongodb_g2 ? "SSD" : "CB2"
}

resource "ncloud_mongodb" "this" {
  count = local.want_mongodb ? 1 : 0

  service_name        = "${var.ncp_name_prefix}-mongodb"
  server_name_prefix  = "${var.ncp_name_prefix}-mongo"
  user_name           = var.ncp_db_username
  user_password       = local.db_password
  vpc_no              = local.vpc_no
  subnet_no           = local.subnet_no
  cluster_type_code   = "STAND_ALONE"
  member_port         = local.mongodb_port
  compress_code       = "SNPP"
  data_storage_type   = local.mongodb_data_storage_type
  engine_version_code = var.ncp_mongodb_version
  image_product_code  = var.ncp_mongodb_image_product_code == "" ? null : var.ncp_mongodb_image_product_code
  member_product_code = var.ncp_mongodb_product_code == "" ? null : var.ncp_mongodb_product_code

  lifecycle {
    precondition {
      condition     = local.mongodb_version_ok
      error_message = "TF_VAR_ncp_mongodb_version='${var.ncp_mongodb_version}' is not in the supported list. It must be a full version string such as 7.0.28; run ./scripts/ncp-db-versions.sh mongodb to list them."
    }
    precondition {
      condition     = local.mongodb_image_ok
      error_message = "TF_VAR_ncp_mongodb_image_product_code='${var.ncp_mongodb_image_product_code}' is not an image of MongoDB ${var.ncp_mongodb_version}. Run ./scripts/ncp-db-versions.sh mongodb and copy an [image ...] code."
    }
    # STAND_ALONE runs one member server, so only a member spec fits. A config
    # server, mongos or arbiter code is refused by NCP 30 minutes in with
    # 5001234 "The product code could not be found."; the role is in the code.
    precondition {
      condition     = !can(regex("\\.(CFGSV|MNGOS|ARBIT)\\.", var.ncp_mongodb_product_code))
      error_message = "TF_VAR_ncp_mongodb_product_code='${var.ncp_mongodb_product_code}' is a config server / mongos / arbiter spec. The STAND_ALONE cluster needs a member spec; run ./scripts/ncp-db-versions.sh mongodb, which lists member specs only."
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
  # An engine that is not wanted has no resource, and every value below is null.
  mysql_master = try(
    [for s in ncloud_mysql.this[0].mysql_server_list : s if s.server_role == "M"][0],
    ncloud_mysql.this[0].mysql_server_list[0],
    null
  )
  postgres_primary = try(
    [for s in ncloud_postgresql.this[0].postgresql_server_list : s if s.server_role == "M"][0],
    ncloud_postgresql.this[0].postgresql_server_list[0],
    null
  )
  mongodb_server = try(ncloud_mongodb.this[0].mongodb_server_list[0], null)

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
  count = local.want_mysql ? 1 : 0

  access_control_group_no = ncloud_mysql.this[0].access_control_group_no_list[0]

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
  count = local.want_postgres ? 1 : 0

  access_control_group_no = ncloud_postgresql.this[0].access_control_group_no_list[0]

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
  count = local.want_mongodb ? 1 : 0

  access_control_group_no = ncloud_mongodb.this[0].access_control_group_no_list[0]

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

# The engines were single resources before they took a count. These carry an
# environment provisioned back then over to index [0]; without them every managed
# DB would be planned for a 30-minute re-creation.
moved {
  from = ncloud_mysql.this
  to   = ncloud_mysql.this[0]
}
moved {
  from = ncloud_postgresql.this
  to   = ncloud_postgresql.this[0]
}
moved {
  from = ncloud_mongodb.this
  to   = ncloud_mongodb.this[0]
}
moved {
  from = ncloud_access_control_group_rule.mysql
  to   = ncloud_access_control_group_rule.mysql[0]
}
moved {
  from = ncloud_access_control_group_rule.postgresql
  to   = ncloud_access_control_group_rule.postgresql[0]
}
moved {
  from = ncloud_access_control_group_rule.mongodb
  to   = ncloud_access_control_group_rule.mongodb[0]
}
