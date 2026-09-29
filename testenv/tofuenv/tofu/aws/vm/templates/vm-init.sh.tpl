#!/bin/bash
# AWS VM init: mount the EFS file system over NFS.
#   Rendered only when TF_VAR_aws_nfs_enabled=true.
#   The mount targets can take a minute or two after creation before their DNS
#   name resolves inside the VPC, so the mount is retried rather than tried once.
#   The mounted root is handed to the login account: SFTP does not sudo, and EFS
#   creates it owned by root.
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

# Boot-time unattended-upgrades can hold the apt lock, so the install is retried.
for i in $(seq 1 30); do
    if apt-get update -y && apt-get install -y nfs-common; then
        break
    fi
    sleep 10
done
command -v mount.nfs4 >/dev/null

mkdir -p "${mount_path}"
if ! grep -qs " ${mount_path} nfs4 " /etc/fstab; then
    echo "${nfs_dns_name}:/ ${mount_path} nfs4 nfsvers=4.1,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2,noresvport,_netdev 0 0" >> /etc/fstab
fi

for i in $(seq 1 30); do
    if mountpoint -q "${mount_path}" || mount "${mount_path}"; then
        break
    fi
    sleep 10
done
mountpoint -q "${mount_path}"

chown "${ssh_user}:${ssh_user}" "${mount_path}"
chmod 755 "${mount_path}"
