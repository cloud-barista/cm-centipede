#!/bin/bash
# NCP server init: enable key-based root SSH and create the test data path.
#   The NCP authentication key (pem) exists to retrieve the root password; key-based
#   SSH is not configured out of the box, so the public key is written straight into
#   authorized_keys.
#   sshd_config itself is left untouched in favour of a drop-in: Ubuntu includes
#   /etc/ssh/sshd_config.d/*.conf near the top of sshd_config, and the first value
#   read wins.
set -e

mkdir -p /root/.ssh
chmod 700 /root/.ssh
echo "${ssh_public_key}" >> /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys

mkdir -p /etc/ssh/sshd_config.d
printf 'PermitRootLogin prohibit-password\nPubkeyAuthentication yes\n' > /etc/ssh/sshd_config.d/99-cm-centipede.conf

# Grow the root filesystem into the whole boot disk. A boot disk larger than the
# image (ncp_vm_volume_size) can come up with the root partition still at the
# image's size, depending on the image. Every step is a no-op when there is
# nothing to grow, and any failure is left alone: a VM with a small root is still
# a working VM, where an init script that stops here would leave SSH unconfigured.
root_src="$(findmnt -n -o SOURCE / || true)"
root_dev="$(basename "$root_src")"
if [ -n "$root_dev" ] && [ -f "/sys/class/block/$root_dev/partition" ] && command -v growpart >/dev/null 2>&1; then
    disk="$(lsblk -no PKNAME "$root_src" | head -1)"
    part="$(cat "/sys/class/block/$root_dev/partition")"
    growpart "/dev/$disk" "$part" || true   # exits 1 with NOCHANGE when already full size
    case "$(findmnt -n -o FSTYPE /)" in
        ext4|ext3) resize2fs "$root_src" || true ;;
        xfs)       xfs_growfs / || true ;;
    esac
fi

mkdir -p "${data_path}"
chmod 755 "${data_path}"

systemctl restart ssh 2>/dev/null || systemctl restart sshd 2>/dev/null || true
