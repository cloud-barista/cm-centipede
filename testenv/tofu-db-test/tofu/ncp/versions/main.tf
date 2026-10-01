# ---------------------------------------------------------------------------
# NCP catalog lookup - data sources only, it creates nothing, it costs nothing.
# ---------------------------------------------------------------------------
#   Answers what the region offers for the target axis of the matrix, so
#   NCP_<engine>_DST_VERSIONS is filled in from the catalog rather than guessed.
#
#   Unlike AWS, NCP does list every version, so ./csp-support-versions.sh ncp
#   --write can rewrite the env keys from this output directly.
#
#   Versions must be full strings, e.g. 8.0.36. The provider normalizes state to
#   the version the API reports and engine_version_code forces replacement, so a
#   partial version schedules a ~30 minute re-creation on every later plan.
# ---------------------------------------------------------------------------

data "ncloud_mysql_image_products" "all" {}

data "ncloud_postgresql_image_products" "all" {}

data "ncloud_mongodb_image_products" "all" {}

# One engine version can carry several image product codes (different
# generations of the same image). Group by version with the ellipsis and join
# the codes, so the output stays a map of strings.
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
}
