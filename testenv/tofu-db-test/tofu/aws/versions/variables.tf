variable "aws_region" {
  description = "AWS region the catalog is queried in"
  type        = string
  default     = "ap-northeast-2"
}

# Version lines to probe, oldest first, so the summary is not bounded below by the
# default version.
#
#   The problem this solves: valid_minor_targets / valid_major_targets are UPGRADE
#   targets, so a list built from the default version only ever runs forward. With a
#   default of MariaDB 11.8 the 10.x lines are invisible even though RDS orders them.
#
#   How it works: `version` is passed to DescribeDBEngineVersions as given and RDS
#   matches it as a prefix, so "10" selects every 10.x; `latest = true` then narrows
#   that to one. Each probe contributes its own version plus its upgrade targets.
#   (preferred_versions cannot be used here — the provider compares it with exact
#   string equality, and the exact patch levels are what we are trying to discover.)
#
#   Minor lines, not major families: `latest` picks ONE version per probe, so a
#   probe of "10" returns the newest 10.x and 10.6 stays invisible - which is the
#   very case this exists for. One probe per line that should be visible.
#
#   ⚠ A line the region no longer offers fails this lookup: the data source errors
#     rather than returning nothing, and OpenTofu cannot catch that. The cost is
#     limited - `_versions_json` in lib/tofu.sh warns and returns an empty catalog,
#     and the matrix falls through to its per-version `tofu plan`, which is the
#     authoritative check anyway. So a retired line degrades the summary; it does
#     not stop a run. Drop the line from this map when that happens, or set the map
#     to {} to fall back to the default-and-upward summary.
variable "probe_version_lines" {
  description = "Per engine, the version lines to probe oldest-first; keyed by the RDS engine name"
  type        = map(list(string))

  default = {
    mysql    = ["8.0", "8.4"]
    mariadb  = ["10.6", "10.11", "11.4", "11.8"]
    postgres = ["13", "14", "15", "16", "17"]
  }
}

variable "db_instance_class" {
  description = "The instance class to check for orderability; the fallbacks are listed after it"
  type        = string
  default     = "db.t3.micro"
}
