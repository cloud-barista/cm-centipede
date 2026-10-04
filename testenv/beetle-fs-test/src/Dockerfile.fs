# beetle-fs-test — the source machine
#
# Ubuntu 22.04 with systemd as PID 1, so `systemctl` works, sshd is a unit and
# the dataset is built by a unit at boot. A source here plays the part of an
# on-premises machine, which an official one-process image cannot do.
#
# This is dockerenv's compose/Dockerfile.filesystem with one change: SSH key
# authentication replaces the password. Everything else that matters was already
# there, because cm-honeybee installs its own agent over SSH and the agent needs
# a real machine to be installed on.
#
# ── What cm-honeybee needs from this image, and why ─────────────────────────
# Registering an ssh connection makes honeybee install its agent here
# (pkg/api/rest/controller/connectionInfo.go, the default branch of
# doGetConnectionInfo → ssh.RunAgent). That install is not a package install:
#
#   1. honeybee SFTPs its own busybox and copyAgent.sh into /tmp
#   2. it runs `sudo /tmp/copyAgent.sh`
#   3. the script downloads the agent binary, its conf and its systemd unit,
#      then `systemctl enable --now cm-honeybee-agent`
#   4. honeybee polls `curl localhost:8082/honeybee-agent/readyz`, 30 times
#
# So the image needs, and installs, exactly this much:
#
#   systemd     step 3 runs systemctl
#   openssh     steps 1-4 all arrive over SSH; step 1 needs the sftp subsystem
#   root        copyAgent.sh exits immediately when EUID is not 0
#   sudo        step 2 invokes it by name
#   curl        step 4, and every later inspect, runs curl ON THIS HOST
#   rsync       not honeybee's — transx-ex rsyncs the data out over SSH
#
# ⚠ The agent binary is downloaded from the internet at install time, from a
#   URL hard-coded in honeybee's copyAgent.sh. This container therefore needs
#   outbound access to raw.githubusercontent.com and media.githubusercontent.com.
#   Nothing here can supply it offline — the URL is not ours to set.
FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=Asia/Seoul

RUN apt-get update && apt-get install -y \
    systemd \
    systemd-sysv \
    dbus \
    openssh-server \
    ca-certificates \
    curl \
    rsync \
    sudo \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# ── Remove systemd units a container has no use for ───────────────────────────
# A getty that keeps restarting or a udev that cannot see the host's devices
# makes `systemctl --failed` noisy, and that output is what src_wait_ready prints
# when a boot does not settle.
RUN find /etc/systemd/system /lib/systemd/system \
    -path '*.wants/*' \( \
    -name '*getty*' -o \
    -name '*plymouth*' -o \
    -name '*apt-daily*' -o \
    -name 'systemd-timesyncd*' -o \
    -name '*udev*' \
    \) -exec rm -f {} \; 2>/dev/null || true

# ── SSH: key authentication only ──────────────────────────────────────────────
# Passwords are turned off rather than merely unused. cm-honeybee refuses a
# password on an fs connection (HTTP 400) and cm-centipede authenticates SSH by
# key only, so a source reachable only by password cannot be registered at all.
# Refusing passwords here as well keeps the image honest about that: a missing
# key fails at the first login, not at a password nothing downstream will take.
#
# PermitRootLogin prohibit-password is what lets copyAgent.sh run: honeybee logs
# in as the connection's user and calls sudo, and root is the one user for which
# that needs nothing configured.
RUN mkdir -p /var/run/sshd /root/.ssh \
    && chmod 700 /root/.ssh \
    && ssh-keygen -A \
    && sed -i 's/^#*PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config \
    && sed -i 's/^#*PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config \
    && sed -i 's/^#*PubkeyAuthentication.*/PubkeyAuthentication yes/' /etc/ssh/sshd_config \
    && sed -i 's/^#*UseDNS.*/UseDNS no/' /etc/ssh/sshd_config \
    && if ! grep -q '^UseDNS' /etc/ssh/sshd_config; then echo 'UseDNS no' >> /etc/ssh/sshd_config; fi

# ── The matrix's own files ────────────────────────────────────────────────────
# defaults.env is what the container falls back to when the matrix's per-run env
# file did not mount. It exists so the image is runnable on its own; the run
# itself always mounts over it, and assert_source_mounts refuses a boot where the
# mount silently became a directory.
COPY scripts/init-fs.sh /opt/matrix/init-fs.sh
COPY scripts/seed-fs.sh /opt/matrix/seed-fs.sh
COPY services/matrix-init.service /etc/systemd/system/matrix-init.service

RUN printf 'FS_SRC_PATH="/testdata"\n' > /opt/matrix/defaults.env \
    && chmod +x /opt/matrix/*.sh

RUN systemctl enable matrix-init.service ssh.service

EXPOSE 22
# lib/source.sh rebuilds a cached image whose revision differs from its own
# SRC_IMAGE_REVISION, so an image from before a structural change is not reused.
LABEL beetle-fs-test.revision="env-credentials"

STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd", "--system"]
