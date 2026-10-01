# ---------------------------------------------------------------------------
# AWS catalog lookup - data sources only, it creates nothing, it costs nothing.
# ---------------------------------------------------------------------------
#   Answers "what can this region actually give me" for the target axis of the
#   matrix, so <CSP>_<engine>_DST_VERSIONS is filled in from the region rather
#   than guessed.
#
#   Limitation, and why the matrix does not rely on this alone: the AWS
#   provider has no data source that lists every engine version -
#   aws_rds_engine_version returns exactly one. What is reported is the default
#   version, the latest version and the upgrade targets reachable from the
#   default. The authoritative per-version check happens in tofu/aws/rdbms,
#   where a plan for the exact version fails when the region does not offer it.
#
#   For the exhaustive list:
#     aws rds describe-db-engine-versions --engine mysql --region <region> \
#       --query 'DBEngineVersions[].EngineVersion'
#
#   Run: ./csp-support-versions.sh aws
# ---------------------------------------------------------------------------

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

# ---------------------------------------------------------------------------
# Version-line probes - what widens the summary downward.
#
#   One data source per (engine, line) in var.probe_version_lines. `version` is
#   handed to the API as a prefix and `latest` picks the newest match in that line,
#   so "10.6" yields the current newest 10.6.x. Each probe also carries its own
#   upgrade targets, which fills in the versions between the lines.
#
#   Without this, the list starts at the default version and only runs forward -
#   see the comment on probe_version_lines for why that hides whole engine families.
# ---------------------------------------------------------------------------
locals {
  probes = {
    for p in flatten([
      for engine, lines in var.probe_version_lines : [
        for line in lines : { key = "${engine}-${line}", engine = engine, line = line }
      ]
    ]) : p.key => p
  }
}

data "aws_rds_engine_version" "probe" {
  for_each = local.probes

  engine  = each.value.engine
  version = each.value.line
  latest  = true
}

# aws_rds_orderable_db_instance returns ONE class: the first entry of
# preferred_instance_classes the engine actually offers here. It is a check of
# whether db_instance_class is orderable, with fallbacks listed after it.
locals {
  preferred_db_classes = [
    var.db_instance_class,
    "db.t4g.micro",
    "db.t3.small",
    "db.t4g.small",
    "db.t3.medium",
  ]
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

locals {
  engine_versions = {
    mysql    = { default = data.aws_rds_engine_version.mysql_default, latest = data.aws_rds_engine_version.mysql_latest }
    mariadb  = { default = data.aws_rds_engine_version.mariadb_default, latest = data.aws_rds_engine_version.mariadb_latest }
    postgres = { default = data.aws_rds_engine_version.postgres_default, latest = data.aws_rds_engine_version.postgres_latest }
  }

  # csp-support-versions.sh reads these field names explicitly, so the script
  # controls the print order rather than jq's key order.
  version_report = {
    for engine, v in local.engine_versions : engine => {
      default_version        = v.default.version_actual
      latest_version         = v.latest.version_actual
      minor_upgrade_targets  = length(v.default.valid_minor_targets) > 0 ? join(" ", sort(tolist(v.default.valid_minor_targets))) : ""
      major_upgrade_targets  = length(v.default.valid_major_targets) > 0 ? join(" ", sort(tolist(v.default.valid_major_targets))) : ""
      parameter_group_family = v.default.parameter_group_family

      # Which lines were probed, so a summary that still looks short can be read
      # against what was actually asked for.
      probed_lines = join(" ", lookup(var.probe_version_lines, engine, []))
    }
  }

  # What each engine's probes found: the newest version of every probed line, plus
  # the upgrade targets reachable from each of them.
  probed = {
    for engine, _ in local.engine_versions : engine => distinct(concat(
      [for k, d in data.aws_rds_engine_version.probe : d.version_actual
      if local.probes[k].engine == engine],
      flatten([for k, d in data.aws_rds_engine_version.probe :
        concat(tolist(d.valid_minor_targets), tolist(d.valid_major_targets))
      if local.probes[k].engine == engine]),
    ))
  }

  # The candidate list the script may write into check-matrix.env: the default plus
  # everything reachable from it, widened by the probes so lines older than the
  # default are included too. Sorted and de-duplicated.
  suggested = {
    for engine, v in local.engine_versions : engine => distinct(concat(
      [v.default.version_actual],
      sort(tolist(v.default.valid_minor_targets)),
      sort(tolist(v.default.valid_major_targets)),
      lookup(local.probed, engine, []),
    ))
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
