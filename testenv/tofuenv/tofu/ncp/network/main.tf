# ---------------------------------------------------------------------------
# NCP network (prerequisite module)
#   NCP has no equivalent of the AWS default VPC, so the VPC, subnet and ACG are
#   created explicitly. The vm and database modules look these up by name through
#   data sources, so their state stays separate but this module must be applied first.
#
#   The subnet is PUBLIC on purpose: a public domain for a managed NCP database can
#   only be requested for a DB server that lives in a PUBLIC subnet.
# ---------------------------------------------------------------------------
locals {
  vpc_name    = "${var.ncp_name_prefix}-vpc"
  subnet_name = "${var.ncp_name_prefix}-subnet"
  acg_name    = "${var.ncp_name_prefix}-server-acg"
}

resource "ncloud_vpc" "this" {
  name            = local.vpc_name
  ipv4_cidr_block = var.ncp_vpc_cidr
}

resource "ncloud_subnet" "public" {
  vpc_no         = ncloud_vpc.this.vpc_no
  name           = local.subnet_name
  subnet         = cidrsubnet(var.ncp_vpc_cidr, 8, 1) # e.g. 10.10.0.0/16 -> 10.10.1.0/24
  zone           = var.ncp_zone
  network_acl_no = ncloud_vpc.this.default_network_acl_no
  subnet_type    = "PUBLIC"
  usage_type     = "GEN"
}

# ---------------------------------------------------------------------------
# ACG for servers, attached to the VM network interface.
#   On NCP an ACG attaches to a network interface, not directly to a server.
#   Provider constraint: only ONE access_control_group_rule resource may target a
#   given ACG - multiple resources overwrite each other. All rules therefore live
#   in this single resource.
# ---------------------------------------------------------------------------
resource "ncloud_access_control_group" "server" {
  name        = local.acg_name
  description = "cm-centipede migration test servers (SSH)"
  vpc_no      = ncloud_vpc.this.vpc_no
}

resource "ncloud_access_control_group_rule" "server" {
  access_control_group_no = ncloud_access_control_group.server.id

  inbound {
    protocol    = "TCP"
    ip_block    = var.allowed_cidr
    port_range  = "22"
    description = "SSH"
  }

  outbound {
    protocol    = "TCP"
    ip_block    = "0.0.0.0/0"
    port_range  = "1-65535"
    description = "all TCP outbound"
  }
}
