# ---------------------------------------------------------------------------
# NCP network - the prerequisite for every NCP column.
#
#   NCP has no equivalent of the AWS default VPC, so a VPC and a subnet are
#   created explicitly. tofu/ncp/rdbms looks them up BY NAME, which keeps the
#   two modules' state separate but means this one has to exist first;
#   lib/tofu.sh applies it before the first column and destroys it after the
#   last one.
#
#   The subnet is PUBLIC on purpose: NCP only issues a public domain for a DB
#   server that sits in a PUBLIC subnet, and without a public domain the matrix
#   host cannot reach the database at all.
#
#   No ACG is created here. Each managed DB brings its own, and the matrix adds
#   an inbound rule to that one in tofu/ncp/rdbms - unlike the tofuenv network
#   module, which also serves VMs.
# ---------------------------------------------------------------------------
locals {
  vpc_name    = "${var.name_prefix}-vpc"
  subnet_name = "${var.name_prefix}-subnet"
}

resource "ncloud_vpc" "this" {
  name            = local.vpc_name
  ipv4_cidr_block = var.ncp_vpc_cidr
}

resource "ncloud_subnet" "public" {
  vpc_no         = ncloud_vpc.this.vpc_no
  name           = local.subnet_name
  subnet         = cidrsubnet(var.ncp_vpc_cidr, 8, 1) # 10.20.0.0/16 -> 10.20.1.0/24
  zone           = var.ncp_zone
  network_acl_no = ncloud_vpc.this.default_network_acl_no
  subnet_type    = "PUBLIC"
  usage_type     = "GEN"
}
