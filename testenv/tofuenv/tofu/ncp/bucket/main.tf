# ---------------------------------------------------------------------------
# NCP Object Storage bucket, used as the object storage migration test target.
#   No VPC is required. The S3-compatible API uses the endpoint and signing region below:
#     endpoint       : kr.object.ncloudstorage.com   (lowercased region code)
#     signing region : kr-standard                   (differs from the provider region KR!)
#   gendata fills the bucket with objects; this module owns the bucket lifecycle.
# ---------------------------------------------------------------------------
locals {
  # The hostname uses the lowercased region code, e.g. KR -> kr.
  os_region_code = lower(var.ncp_region)
  os_endpoint    = "${local.os_region_code}.object.ncloudstorage.com"

  # Region name S3 SDKs and clients use when signing. KR -> kr-standard is confirmed;
  # other regions appear to follow the same <code>-standard rule but are unverified.
  os_signing_region = "${local.os_region_code}-standard"
}

resource "ncloud_objectstorage_bucket" "this" {
  bucket_name = var.ncp_bucket_name
}
