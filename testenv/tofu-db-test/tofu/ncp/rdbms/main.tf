# ---------------------------------------------------------------------------
# One managed NCP database = one column of the version matrix.
#
#   Three engines, three resource types, one of them created per apply. NCP has
#   no managed MariaDB, so a mariadb column is AWS-only.
#
#   Reaching a managed NCP database from outside the VPC needs a public domain,
#   and that can only be requested from the console - there is no API action and
#   no provider argument. Until it is issued, host is null; check-matrix-ncp.sh
#   stops and waits for it, then refreshes this state so the outputs pick it up.
#
#   Every attribute of these resources is RequiresReplace and the provider
#   implements no Update. That is harmless here: a column is created once and
#   destroyed when it ends.
# ---------------------------------------------------------------------------
locals {
  db_password = data.vault_kv_secret_v2.db.data["NCP_DB_PASSWORD"]

  is_mysql    = var.engine == "mysql"
  is_postgres = var.engine == "postgresql"
  is_mongo    = var.engine == "mongodb"

  # Naming. A managed MongoDB service_name is capped at 15 characters, which
  # rules out the readable "<prefix>-<engine>-<8-0-36>" form the AWS module
  # uses. The version still has to be in the name, or a column left behind by
  # --keep-instance would block the next one, so the engine becomes a two-letter
  # code and the version loses its dots:
  #
  #     cptfm-mg7028   mongodb 7.0.28      12 chars
  #     cptfm-my8036   mysql   8.0.36      12 chars
  #     cptfm-pg1422   postgresql 14.22    12 chars
  engine_code = {
    mysql      = "my"
    postgresql = "pg"
    mongodb    = "mg"
  }
  version_digits = replace(var.engine_version, ".", "")
  instance_name  = "${var.name_prefix}-${local.engine_code[var.engine]}${local.version_digits}"

  # Ports match the AWS module so the matrix config stays CSP-agnostic.
  # The managed MongoDB member_port defaults to 17017 but accepts 10000-65535.
  port = local.is_postgres ? 5432 : (local.is_mongo ? 27017 : 3306)
}

# ---------------------------------------------------------------------------
# What the network module created, looked up by name.
# ---------------------------------------------------------------------------
data "ncloud_vpcs" "this" {
  name = "${var.name_prefix}-vpc"
}

data "ncloud_subnets" "public" {
  vpc_no = data.ncloud_vpcs.this.vpcs[0].vpc_no

  # ncloud_subnets has no name argument, so the name is matched by filter.
  filter {
    name   = "name"
    values = ["${var.name_prefix}-subnet"]
  }
}

locals {
  vpc_no    = data.ncloud_vpcs.this.vpcs[0].vpc_no
  subnet_no = data.ncloud_subnets.public.subnets[0].subnet_no
}

# ---------------------------------------------------------------------------
# Fail-fast version check, at plan time.
#   The filter is an exact match, so a partial version like 8.0 is caught here
#   rather than after a 30-minute apply.
# ---------------------------------------------------------------------------
data "ncloud_mysql_image_products" "pinned" {
  count = local.is_mysql ? 1 : 0

  filter {
    name   = "engine_version_code"
    values = [var.engine_version]
  }
}

data "ncloud_postgresql_image_products" "pinned" {
  count = local.is_postgres ? 1 : 0

  filter {
    name   = "engine_version_code"
    values = [var.engine_version]
  }
}

data "ncloud_mongodb_image_products" "pinned" {
  count = local.is_mongo ? 1 : 0

  filter {
    name   = "engine_version_code"
    values = [var.engine_version]
  }
}

locals {
  # try(), not a bare index: a conditional evaluates both of its branches, so
  # indexing the data source that count left empty would fail the plan outright.
  mysql_version_ok    = try(length(data.ncloud_mysql_image_products.pinned[0].image_product_list) > 0, false)
  postgres_version_ok = try(length(data.ncloud_postgresql_image_products.pinned[0].image_product_list) > 0, false)
  mongo_version_ok    = try(length(data.ncloud_mongodb_image_products.pinned[0].image_product_list) > 0, false)

  version_ok = local.is_mysql ? local.mysql_version_ok : (local.is_postgres ? local.postgres_version_ok : local.mongo_version_ok)
}

# ---------------------------------------------------------------------------
# Managed MySQL
#   With is_ha = false, supplying is_multi_zone / standby_master_subnet_no /
#   is_storage_encryption is itself an error, so they are omitted, and so are
#   is_automatic_backup / backup_time under is_backup = false.
#   There is no vpc_no argument; the provider derives it from subnet_no.
# ---------------------------------------------------------------------------
resource "ncloud_mysql" "this" {
  count = local.is_mysql ? 1 : 0

  service_name       = local.instance_name
  server_name_prefix = local.instance_name
  user_name          = var.db_username
  user_password      = local.db_password
  host_ip            = "%" # Any host, so connections through the public domain work.
  database_name      = var.db_name
  subnet_no          = local.subnet_no
  is_ha              = false
  is_backup          = false

  port                = local.port
  engine_version_code = var.engine_version

  lifecycle {
    precondition {
      condition     = local.version_ok
      error_message = "TF_VAR_engine_version is not supported: run ./csp-support-versions.sh ncp for the list."
    }
  }
}

# ---------------------------------------------------------------------------
# Managed PostgreSQL
#   ha / backup default to true in the provider, so they are set to false.
#   client_cidr is required and acts as an access control list.
# ---------------------------------------------------------------------------
resource "ncloud_postgresql" "this" {
  count = local.is_postgres ? 1 : 0

  service_name       = local.instance_name
  server_name_prefix = local.instance_name
  user_name          = var.db_username
  user_password      = local.db_password
  vpc_no             = local.vpc_no
  subnet_no          = local.subnet_no
  client_cidr        = var.allowed_cidr
  database_name      = var.db_name
  ha                 = false
  backup             = false

  port                = local.port
  engine_version_code = var.engine_version

  lifecycle {
    precondition {
      condition     = local.version_ok
      error_message = "TF_VAR_engine_version is not supported: run ./csp-support-versions.sh ncp for the list."
    }
  }
}

# ---------------------------------------------------------------------------
# Managed MongoDB (STAND_ALONE)
#   A replica set is not used: reaching one through a public domain requires
#   editing the client's hosts file and tolerating a brief outage.
#   Under STAND_ALONE, supplying member_server_count / arbiter_* / mongos_* /
#   config_* / shard_count is an error, so they are omitted.
#   compress_code and data_storage_type are stated explicitly: the provider
#   always forwards the first, and never fills the second during Create, which
#   fails the apply with "provider still indicated an unknown value". The value
#   cannot be SSD - NCP answers 400 / returnCode 5001451 for the KR region, G3
#   generation - so CB2, the common block storage generation KR/G3 serves.
# ---------------------------------------------------------------------------
resource "ncloud_mongodb" "this" {
  count = local.is_mongo ? 1 : 0

  service_name       = local.instance_name
  server_name_prefix = local.instance_name
  user_name          = var.db_username
  user_password      = local.db_password
  vpc_no             = local.vpc_no
  subnet_no          = local.subnet_no
  cluster_type_code  = "STAND_ALONE"

  member_port         = local.port
  compress_code       = "SNPP"
  data_storage_type   = "CB2"
  engine_version_code = var.engine_version

  lifecycle {
    precondition {
      condition     = local.version_ok
      error_message = "TF_VAR_engine_version is not supported: run ./csp-support-versions.sh ncp for the list."
    }

    # The name has to fit before the API is called, not after a failed apply.
    precondition {
      condition     = length(local.instance_name) <= 15
      error_message = "The generated MongoDB service_name is longer than the 15 characters NCP allows. Shorten name_prefix (MATRIX_NAME_PREFIX in check-matrix.env)."
    }
  }
}

# ---------------------------------------------------------------------------
# Inbound rule on the ACG NCP created for the instance.
#
#   The provider docs warn that only ONE rule resource may target a given ACG,
#   and NCP fills each of these with its own rules, described as "(automatically
#   created, don't delete it) for the DB service itself". The provider reads
#   every rule present back into state while the configuration declares one, so
#   without ignore_changes each apply plans an in-place update that DELETES
#   NCP's rules and can break the DB service. Their ports are not exposed by any
#   data source, so re-declaring them would mean hard-coding undocumented values.
#
#   Consequence: changing allowed_cidr afterwards has no effect. A matrix column
#   is short-lived and re-created per version, so that costs nothing here.
# ---------------------------------------------------------------------------
locals {
  acg_no_list = (
    local.is_mysql ? try(ncloud_mysql.this[0].access_control_group_no_list, []) : (
      local.is_postgres ? try(ncloud_postgresql.this[0].access_control_group_no_list, []) :
      try(ncloud_mongodb.this[0].access_control_group_no_list, [])
    )
  )

  acg_no = try(local.acg_no_list[0], null)
}

resource "ncloud_access_control_group_rule" "this" {
  access_control_group_no = local.acg_no

  inbound {
    protocol    = "TCP"
    ip_block    = var.allowed_cidr
    port_range  = tostring(local.port)
    description = "cm-centipede version matrix (${var.engine} ${var.engine_version})"
  }

  # ── outbound is declared, not omitted ─────────────────────────────────────
  # inbound and outbound are attributes in "attributes as blocks" mode, not real
  # blocks. In that mode an absent block set becomes an EMPTY set rather than
  # null, and an empty set is the instruction to purge every rule of that kind:
  #
  #   "Some resource types ... will purge any sub-objects of that type if that
  #    argument is set to an empty list."
  #
  # So declaring only inbound asked NCP to delete the outbound rules it created
  # for its own DB service. Declared permissively here instead, across all three
  # protocols the resource accepts (there is no "ALL"), so nothing NCP put there
  # is removed. ICMP takes no port range.
  outbound {
    protocol    = "TCP"
    ip_block    = "0.0.0.0/0"
    port_range  = "1-65535"
    description = "cm-centipede version matrix - outbound TCP"
  }

  outbound {
    protocol    = "UDP"
    ip_block    = "0.0.0.0/0"
    port_range  = "1-65535"
    description = "cm-centipede version matrix - outbound UDP (DNS)"
  }

  outbound {
    protocol    = "ICMP"
    ip_block    = "0.0.0.0/0"
    description = "cm-centipede version matrix - outbound ICMP"
  }

  lifecycle {
    # NCP fills this ACG with rules of its own, described as "(automatically
    # created, don't delete it) for the DB service itself". The provider reads
    # every rule present back into state while the configuration declares a few,
    # so without this each apply would plan to remove NCP's. It does not affect
    # creation, where the configuration above is what gets applied.
    ignore_changes = [inbound, outbound]

    # ⚠ The rule can only be attached to ONE ACG, and access_control_group_no_list
    #   is a list whose order NCP does not promise. Taking [0] out of a list with
    #   several entries would open the engine port on an ACG that governs nothing,
    #   leave the one that does untouched, and still report success - an instance
    #   that applies cleanly and cannot be reached. Rather than guess, this stops
    #   with the numbers in hand. It is checked at apply time, since the list is
    #   unknown while the instance is still being planned.
    precondition {
      condition = length(local.acg_no_list) == 1
      error_message = format(
        "expected the %s instance to have exactly one access control group, found %d: %s. The inbound rule can target only one, and the list order is not guaranteed - see tofu/ncp/rdbms/main.tf.",
        var.engine, length(local.acg_no_list), join(", ", local.acg_no_list)
      )
    }
  }
}

# ---------------------------------------------------------------------------
# Pick the master / primary server.
#   For MySQL and PostgreSQL server_role is a code (M/H/S); for MongoDB it is a
#   CodeName ("Member"), so a role filter cannot be used there - and a
#   STAND_ALONE MongoDB has exactly one server anyway.
# ---------------------------------------------------------------------------
locals {
  master = (
    local.is_mysql ? try(
      [for s in ncloud_mysql.this[0].mysql_server_list : s if s.server_role == "M"][0],
      ncloud_mysql.this[0].mysql_server_list[0],
      null
      ) : (
      local.is_postgres ? try(
        [for s in ncloud_postgresql.this[0].postgresql_server_list : s if s.server_role == "M"][0],
        ncloud_postgresql.this[0].postgresql_server_list[0],
        null
        ) : try(
        ncloud_mongodb.this[0].mongodb_server_list[0],
        null
      )
    )
  )

  # Null until a public domain is issued from the console. An empty string means
  # the same thing, so both become null.
  public_domain_raw = try(local.master.public_domain, null)
  public_domain     = (local.public_domain_raw == null || local.public_domain_raw == "") ? null : local.public_domain_raw

  # MySQL names it is_public_subnet, PostgreSQL public_subnet; MongoDB reports
  # neither. Requesting a public domain requires a PUBLIC subnet, so this is
  # worth surfacing when the domain never appears.
  public_subnet = try(local.master.is_public_subnet, try(local.master.public_subnet, null))
}
