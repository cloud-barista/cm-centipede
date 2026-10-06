# ---------------------------------------------------------------------------
# NCP catalog lookup module - data sources only, it creates nothing.
# ---------------------------------------------------------------------------
#   Purpose: find the exact values to put into .env.
#     - TF_VAR_ncp_{mysql,postgres,mongodb}_version : managed DB engine versions
#     - TF_VAR_ncp_server_image_name / _spec_code   : server image and spec for VM and MariaDB
#     - TF_VAR_ncp_{mysql,postgres,mongodb}_product_code : managed DB server specs,
#       listed for the engine versions .env sets
#
#   Engine versions MUST be full version strings, e.g. 8.0.36.
#     When refreshing state the provider normalizes the version reported by the API
#     with the regex \d+\.\d+(\.\d+)? , and engine_version_code is RequiresReplace.
#     A partial version such as 8.0 therefore makes every plan schedule a DB
#     re-creation that takes about 30 minutes.
#
#   Run: ./scripts/ncp-db-versions.sh
# ---------------------------------------------------------------------------

data "ncloud_mysql_image_products" "all" {}

data "ncloud_postgresql_image_products" "all" {}

data "ncloud_mongodb_image_products" "all" {}

data "ncloud_server_image_numbers" "all" {}

data "ncloud_server_specs" "all" {}

# The catalog can expose several image product codes for one engine version
# (for example different generations of the same 8.4.8 image). Group by version
# with the ellipsis and join the codes so the output stays a map of strings.
locals {
  mysql_versions = {
    for v, codes in {
      for i in data.ncloud_mysql_image_products.all.image_product_list :
      i.engine_version_code => i.product_code...
    } : v => join(", ", distinct(codes))
  }

  postgresql_versions = {
    for v, codes in {
      for i in data.ncloud_postgresql_image_products.all.image_product_list :
      i.engine_version_code => i.product_code...
    } : v => join(", ", distinct(codes))
  }

  mongodb_versions = {
    for v, codes in {
      for i in data.ncloud_mongodb_image_products.all.image_product_list :
      i.engine_version_code => i.product_code...
    } : v => join(", ", distinct(codes))
  }

  server_images = {
    for k, numbers in {
      for i in data.ncloud_server_image_numbers.all.image_number_list :
      "${i.name} (${i.hypervisor_type})" => i.server_image_number...
    } : k => join(", ", distinct(numbers))
  }
}

# ---------------------------------------------------------------------------
# Managed DB server specs for the engine versions .env sets
#   A spec (product code) belongs to an image product, so the version is turned
#   into its image product code(s) first. One version can map to several images
#   (generations), and the specs are listed per image.
#   memory_size comes in bytes, like ncloud_server_specs. disk_type (and on
#   MongoDB the server type) can come back null, so they are shown as "-"; the
#   disk type is part of the product code anyway (...SSD...).
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

data "ncloud_mysql_products" "pinned" {
  for_each           = toset([for i in data.ncloud_mysql_image_products.pinned.image_product_list : i.product_code])
  image_product_code = each.key
}

data "ncloud_postgresql_products" "pinned" {
  for_each           = toset([for i in data.ncloud_postgresql_image_products.pinned.image_product_list : i.product_code])
  image_product_code = each.key
}

data "ncloud_mongodb_products" "pinned" {
  for_each           = toset([for i in data.ncloud_mongodb_image_products.pinned.image_product_list : i.product_code])
  image_product_code = each.key
}

# Each map is "<image product code> (<generation>)" -> the specs of that image.
# A spec only works with its own image: the same version can come as several
# images, and a spec of one is refused when NCP builds the DB from another.
locals {
  mysql_images = {
    for i in data.ncloud_mysql_image_products.pinned.image_product_list :
    i.product_code => format("%s (%s)", i.product_code, coalesce(i.generation_code, "-"))
  }
  postgresql_images = {
    for i in data.ncloud_postgresql_image_products.pinned.image_product_list :
    i.product_code => format("%s (%s)", i.product_code, coalesce(i.generation_code, "-"))
  }
  mongodb_images = {
    for i in data.ncloud_mongodb_image_products.pinned.image_product_list :
    i.product_code => format("%s (%s)", i.product_code, coalesce(i.generation_code, "-"))
  }

  mysql_specs = {
    for code, d in data.ncloud_mysql_products.pinned : local.mysql_images[code] => sort(distinct([
      for p in d.product_list :
      format("%s (%d vCPU, %d GB, %s)", p.product_code, p.cpu_count, floor(p.memory_size / 1073741824), coalesce(p.disk_type, "-"))
    ]))
  }

  postgresql_specs = {
    for code, d in data.ncloud_postgresql_products.pinned : local.postgresql_images[code] => sort(distinct([
      for p in d.product_list :
      format("%s (%d vCPU, %d GB, %s)", p.product_code, p.cpu_count, floor(p.memory_size / 1073741824), coalesce(p.disk_type, "-"))
    ]))
  }

  # MongoDB lists member, arbiter, mongos and config server specs together, and
  # the module's STAND_ALONE cluster takes a member spec only: a config server
  # code (...CFGSV...) as member_product_code answers 5001234, "The product code
  # could not be found." The other roles are named in the code, so they are
  # left out here; the type is still shown.
  mongodb_specs = {
    for code, d in data.ncloud_mongodb_products.pinned : local.mongodb_images[code] => sort(distinct([
      for p in d.product_list :
      format("%s (%s, %d vCPU, %d GB, %s)", p.product_code, coalesce(p.infra_resource_detail_type, "-"), p.cpu_count, floor(p.memory_size / 1073741824), coalesce(p.disk_type, "-"))
      if !can(regex(local.mongodb_non_member, p.product_code))
    ]))
  }

  # The product code field that marks a config server, mongos router or arbiter.
  mongodb_non_member = "\\.(CFGSV|MNGOS|ARBIT)\\."
}
