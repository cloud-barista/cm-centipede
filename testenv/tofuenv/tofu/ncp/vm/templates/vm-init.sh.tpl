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

mkdir -p "${data_path}"
chmod 755 "${data_path}"

systemctl restart ssh 2>/dev/null || systemctl restart sshd 2>/dev/null || true
