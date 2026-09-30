# ---------------------------------------------------------------------------
# NCP VM used as the filesystem migration test target.
#   The VPC, PUBLIC subnet and server ACG created by the network module are looked
#   up by name. On NCP an ACG attaches to a network interface rather than to the
#   server, and the public IP is a separate resource (ncloud_public_ip).
# ---------------------------------------------------------------------------
# NCP sometimes refuses to delete a login key (500 / returnCode 1300) long after the
# server it belonged to is gone, and ncloud_login_key cannot create a name that already
# exists. A per-key random suffix means a key that could not be deleted never blocks the
# next provisioning. random_id rather than timestamp(): the value is kept in state, so
# plans stay stable instead of scheduling a replacement every run.
resource "random_id" "key" {
  byte_length = 3
}

# The key file is named after the login key, random suffix included: every key the
# module issues gets a file of its own, and the file is the one the console lists it as.
locals {
  key_name           = "${var.ncp_name_prefix}-vm-${random_id.key.hex}"
  server_name        = "${var.ncp_name_prefix}-vm"
  container_key_path = "${var.ssh_key_dir}/ncp-${local.key_name}.pem"
  host_key_path      = "ssh_keys/ncp-${local.key_name}.pem" # Relative to the host-side tofuenv directory.
  ssh_user           = "root"                # AWS images use ubuntu; NCP-provided images use root.
}

# --- Look up what the network module created -------------------------------
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

data "ncloud_access_control_groups" "server" {
  name = "${var.ncp_name_prefix}-server-acg"
}

# --- Server image (KVM) ---------------------------------------------------
data "ncloud_server_image_numbers" "this" {
  server_image_name = var.ncp_server_image_name

  filter {
    name   = "hypervisor_type"
    values = ["KVM"]
  }
}

locals {
  vpc_no       = data.ncloud_vpcs.this.vpcs[0].vpc_no
  subnet_no    = data.ncloud_subnets.public.subnets[0].subnet_no
  acg_no       = data.ncloud_access_control_groups.server.access_control_groups[0]
  image_number = try(data.ncloud_server_image_numbers.this.image_number_list[0].server_image_number, null)
}

# --- SSH key -------------------------------------------------------------
# ncloud_login_key only returns the private key, so the public key is derived with
# tls_public_key and injected into authorized_keys by the init script. This keeps a
# single key for both purposes.
# Note: creation fails if key_name already exists in the account.
resource "ncloud_login_key" "vm" {
  key_name = local.key_name
}

data "tls_public_key" "vm" {
  private_key_pem = ncloud_login_key.vm.private_key
}

resource "local_sensitive_file" "vm_key" {
  content         = ncloud_login_key.vm.private_key
  filename        = local.container_key_path
  file_permission = "0600"
}

# --- Init script ----------------------------------------------------------
resource "ncloud_init_script" "vm" {
  name    = "${var.ncp_name_prefix}-vm-init"
  os_type = "LNX"
  content = templatefile("${path.module}/templates/vm-init.sh.tpl", {
    ssh_public_key = data.tls_public_key.vm.public_key_openssh
    data_path      = var.ncp_data_path
  })
}

# --- Network interface, the attachment point for the ACG -------------------
resource "ncloud_network_interface" "vm" {
  name                  = "${var.ncp_name_prefix}-vm-nic"
  description           = "cm-centipede migration test VM"
  subnet_no             = local.subnet_no
  access_control_groups = [local.acg_no]
}

# --- Server ---------------------------------------------------------------
resource "ncloud_server" "vm" {
  subnet_no           = local.subnet_no
  name                = local.server_name
  description         = "cm-centipede filesystem migration test VM"
  server_image_number = local.image_number
  server_spec_code    = var.ncp_server_spec_code
  login_key_name      = ncloud_login_key.vm.key_name
  init_script_no      = ncloud_init_script.vm.id

  network_interface {
    network_interface_no = ncloud_network_interface.vm.id
    order                = 0
  }

  lifecycle {
    precondition {
      condition     = local.image_number != null
      error_message = "No KVM server image matches TF_VAR_ncp_server_image_name='${var.ncp_server_image_name}'. Run ./scripts/ncp-db-versions.sh server to list the available image names."
    }
  }
}

# --- Public IP ------------------------------------------------------------
resource "ncloud_public_ip" "vm" {
  server_instance_no = ncloud_server.vm.instance_no
  description        = "cm-centipede migration test VM"
}
