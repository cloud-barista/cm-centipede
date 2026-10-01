# ---------------------------------------------------------------------------
# NCP catalog lookup module - data sources only, it creates nothing.
# ---------------------------------------------------------------------------
#   Purpose: find the exact values to put into .env.
#     - TF_VAR_ncp_{mysql,postgres,mongodb}_version : managed DB engine versions
#     - TF_VAR_ncp_server_image_name / _spec_code   : server image and spec for VM and MariaDB
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
