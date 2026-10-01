#!/bin/bash

# Environment setup script for transx-ex file-migration-async example
# Sets up two SSH containers:
#   - transx-migrate-src  (port 3222) — source server with test data
#   - transx-migrate-dst  (port 3223) — destination server (empty)

set -e

# ── Colors ────────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_status()  { echo -e "${BLUE}[INFO]${NC} $1"; }
print_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
print_warning() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
print_error()   { echo -e "${RED}[ERROR]${NC} $1"; }

# ── Constants ─────────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

SRC_CONTAINER="transx-migrate-src"
DST_CONTAINER="transx-migrate-dst"
SRC_PORT=3222
DST_PORT=3223
SSH_KEY_DIR="${SCRIPT_DIR}/ssh_keys"
SRC_DATA_PATH="/data/src"
DST_DATA_PATH="/data/dst"
# Override with env var for remote host (e.g. SSH_HOST=192.168.1.10)
SSH_HOST="${SSH_HOST:-localhost}"

echo -e "${BLUE}=========================================${NC}"
echo -e "${BLUE}  transx-ex file-migration-async — Setup ${NC}"
echo -e "${BLUE}=========================================${NC}"

# ── Tool checks ───────────────────────────────────────────────────────────────

check_tools() {
    print_status "Checking required tools..."

    if ! command -v docker &>/dev/null; then
        print_error "Docker is not installed:"
        echo "    curl -sSL get.docker.com | sh && sudo usermod -aG docker \${USER}"
        exit 1
    fi
    print_success "Docker is installed"

    if ! docker info &>/dev/null; then
        print_error "Docker daemon is not running:"
        echo "    sudo systemctl start docker"
        exit 1
    fi
    print_success "Docker daemon is running"

    if ! command -v ssh-keygen &>/dev/null; then
        print_error "ssh-keygen is not installed (install openssh-client)"
        exit 1
    fi
    print_success "ssh-keygen is available"

    if ! command -v rsync &>/dev/null; then
        print_error "rsync is not installed (required for relay transfer):"
        echo "    sudo apt-get install rsync   # Ubuntu/Debian"
        echo "    brew install rsync           # macOS"
        exit 1
    fi
    print_success "rsync is available"
}

# ── SSH keypair ───────────────────────────────────────────────────────────────

generate_ssh_keys() {
    print_status "Generating SSH keypair..."
    mkdir -p "${SSH_KEY_DIR}"

    if [[ -f "${SSH_KEY_DIR}/id_rsa" ]]; then
        print_success "SSH keypair already exists"
        return
    fi

    ssh-keygen -t rsa -b 4096 -f "${SSH_KEY_DIR}/id_rsa" -N "" -C "transx-migrate-test" &>/dev/null
    chmod 600 "${SSH_KEY_DIR}/id_rsa"
    chmod 644 "${SSH_KEY_DIR}/id_rsa.pub"
    print_success "SSH keypair generated: ${SSH_KEY_DIR}/id_rsa"
}

# ── Container image ───────────────────────────────────────────────────────────

build_image() {
    print_status "Building SSH+rsync container image..."

    # Ubuntu 22.04 with openssh-server and rsync (needed for relay transfer)
    docker build -t transx-migrate:latest - <<'DOCKERFILE'
FROM ubuntu:22.04
RUN apt-get update && apt-get install -y --no-install-recommends openssh-server rsync \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /var/run/sshd /root/.ssh \
    && chmod 700 /root/.ssh \
    && sed -i 's/#PermitRootLogin prohibit-password/PermitRootLogin yes/' /etc/ssh/sshd_config \
    && sed -i 's/#PubkeyAuthentication yes/PubkeyAuthentication yes/'    /etc/ssh/sshd_config \
    && sed -i 's/#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config
EXPOSE 22
CMD ["/usr/sbin/sshd", "-D"]
DOCKERFILE

    print_success "Container image built: transx-migrate:latest"
}

install_pubkey() {
    local container="$1"
    docker cp "${SSH_KEY_DIR}/id_rsa.pub" "${container}:/root/.ssh/authorized_keys"
    docker exec "${container}" chmod 600 /root/.ssh/authorized_keys
}

# ── Source container ──────────────────────────────────────────────────────────

start_src_container() {
    print_status "Starting source container (${SRC_CONTAINER})..."

    if docker ps -a --format '{{.Names}}' | grep -q "^${SRC_CONTAINER}$"; then
        print_warning "Removing existing container: ${SRC_CONTAINER}"
        docker rm -f "${SRC_CONTAINER}" &>/dev/null
    fi

    docker run -d --name "${SRC_CONTAINER}" \
        -p "${SRC_PORT}:22" \
        transx-migrate:latest

    sleep 2
    install_pubkey "${SRC_CONTAINER}"
    print_success "Source container running (${SSH_HOST}:${SRC_PORT})"
}

create_src_test_data() {
    print_status "Creating test data in source container..."

    docker exec "${SRC_CONTAINER}" bash -c "
        mkdir -p /data/src/project-alpha/src
        mkdir -p /data/src/project-alpha/docs
        mkdir -p /data/src/shared/config
        mkdir -p /data/src/tmp
        mkdir -p /data/src/logs
        echo 'package main' > /data/src/project-alpha/src/main.go
        echo '# README' > /data/src/project-alpha/docs/README.md
        echo 'spec: v1' > /data/src/shared/config/app.yaml
        echo 'scratch' > /data/src/tmp/scratch.txt
        echo '[2026-01-01] started' > /data/src/logs/app.log
    "

    print_success "Test data created in ${SRC_DATA_PATH}:"
    echo "  /data/src/project-alpha/src/main.go"
    echo "  /data/src/project-alpha/docs/README.md"
    echo "  /data/src/shared/config/app.yaml"
    echo "  /data/src/tmp/scratch.txt      (exclude target)"
    echo "  /data/src/logs/app.log         (exclude target)"
}

# ── Destination container ─────────────────────────────────────────────────────

start_dst_container() {
    print_status "Starting destination container (${DST_CONTAINER})..."

    if docker ps -a --format '{{.Names}}' | grep -q "^${DST_CONTAINER}$"; then
        print_warning "Removing existing container: ${DST_CONTAINER}"
        docker rm -f "${DST_CONTAINER}" &>/dev/null
    fi

    docker run -d --name "${DST_CONTAINER}" \
        -p "${DST_PORT}:22" \
        transx-migrate:latest

    sleep 2
    install_pubkey "${DST_CONTAINER}"
    docker exec "${DST_CONTAINER}" mkdir -p "${DST_DATA_PATH}"
    print_success "Destination container running (${SSH_HOST}:${DST_PORT})"
}

# ── Config generation ─────────────────────────────────────────────────────────

generate_configs() {
    print_status "Generating config file..."

    cat > "${SCRIPT_DIR}/config-ssh2ssh.json" <<EOF
{
  "source": {
    "storageType": "filesystem",
    "path": "${SRC_DATA_PATH}",
    "filesystem": {
      "accessType": "ssh",
      "ssh": {
        "host": "${SSH_HOST}",
        "port": ${SRC_PORT},
        "username": "root",
        "privateKeyPath": "${SSH_KEY_DIR}/id_rsa"
      }
    },
    "filter": {
      "exclude": ["tmp", "logs"]
    }
  },
  "destination": {
    "storageType": "filesystem",
    "path": "${DST_DATA_PATH}",
    "filesystem": {
      "accessType": "ssh",
      "ssh": {
        "host": "${SSH_HOST}",
        "port": ${DST_PORT},
        "username": "root",
        "privateKeyPath": "${SSH_KEY_DIR}/id_rsa"
      }
    }
  },
  "strategy": "relay"
}
EOF

    print_success "Config generated: config-ssh2ssh.json"
}

# ── SSH connectivity test ─────────────────────────────────────────────────────

test_ssh_connectivity() {
    print_status "Testing SSH connectivity..."
    local ok=true

    for port in "${SRC_PORT}" "${DST_PORT}"; do
        if ssh -i "${SSH_KEY_DIR}/id_rsa" \
               -o StrictHostKeyChecking=no \
               -o ConnectTimeout=5 \
               -p "${port}" root@"${SSH_HOST}" \
               echo "SSH OK" &>/dev/null 2>&1; then
            print_success "SSH to ${SSH_HOST}:${port} OK"
        else
            print_error "SSH to ${SSH_HOST}:${port} failed"
            ok=false
        fi
    done

    [[ "$ok" == true ]]
}

# ── Migration verification ────────────────────────────────────────────────────

verify_migration() {
    print_status "Verifying migration result..."

    echo ""
    echo "Source (${SRC_CONTAINER}:${SRC_DATA_PATH}):"
    docker exec "${SRC_CONTAINER}" find "${SRC_DATA_PATH}" -type f | sort | sed 's/^/  /'

    echo ""
    echo "Destination (${DST_CONTAINER}:${DST_DATA_PATH}):"
    local dst_files
    dst_files=$(docker exec "${DST_CONTAINER}" find "${DST_DATA_PATH}" -type f 2>/dev/null | sort)
    if [[ -z "$dst_files" ]]; then
        echo "  (empty — run migrate.sh first)"
    else
        echo "$dst_files" | sed 's/^/  /'
    fi
}

# ── Cleanup ───────────────────────────────────────────────────────────────────

cleanup() {
    print_status "Cleaning up..."

    for name in "${SRC_CONTAINER}" "${DST_CONTAINER}"; do
        if docker ps -a --format '{{.Names}}' | grep -q "^${name}$"; then
            docker rm -f "${name}" &>/dev/null
            print_success "Container removed: ${name}"
        fi
    done

    if [[ -d "${SSH_KEY_DIR}" ]]; then
        rm -rf "${SSH_KEY_DIR}"
        print_success "SSH keys removed"
    fi

    [[ -f "${SCRIPT_DIR}/config-ssh2ssh.json" ]] && rm -f "${SCRIPT_DIR}/config-ssh2ssh.json" \
        && print_success "Removed: config-ssh2ssh.json"
}

# ── Status display ────────────────────────────────────────────────────────────

display_status() {
    echo ""
    echo -e "${BLUE}=========================================${NC}"
    echo -e "${GREEN}Environment Setup Completed!${NC}"
    echo -e "${BLUE}=========================================${NC}"

    echo ""
    echo -e "${YELLOW}Source Container (${SRC_CONTAINER}):${NC}"
    echo -e "  Host        : ${SSH_HOST}:${SRC_PORT}"
    echo -e "  Username    : root"
    echo -e "  Test Path   : ${SRC_DATA_PATH}"
    echo -e "  Private Key : ${SSH_KEY_DIR}/id_rsa"

    echo ""
    echo -e "${YELLOW}Destination Container (${DST_CONTAINER}):${NC}"
    echo -e "  Host        : ${SSH_HOST}:${DST_PORT}"
    echo -e "  Username    : root"
    echo -e "  Target Path : ${DST_DATA_PATH}"

    echo ""
    echo -e "${YELLOW}Run migration:${NC}"
    echo -e "  ${GREEN}./migrate.sh -c config-ssh2ssh.json${NC}"
    echo -e "  ${GREEN}./migrate.sh -c config-ssh2ssh.json -v${NC}"

    echo ""
    echo -e "${YELLOW}Verify result:${NC}"
    echo -e "  ${GREEN}./setup_environment.sh verify${NC}"
}

# ── Main ──────────────────────────────────────────────────────────────────────

case "${1:-all}" in
    "all")
        check_tools
        generate_ssh_keys
        build_image
        start_src_container
        create_src_test_data
        start_dst_container
        test_ssh_connectivity
        generate_configs
        display_status
        ;;
    "cleanup")
        cleanup
        ;;
    "test-ssh")
        test_ssh_connectivity
        ;;
    "verify")
        verify_migration
        ;;
    *)
        echo -e "${RED}Usage: $0 [all|cleanup|test-ssh|verify]${NC}"
        echo -e "${YELLOW}  all      - Setup src/dst containers with test data (default)${NC}"
        echo -e "${YELLOW}  cleanup  - Stop containers and remove SSH keys / generated configs${NC}"
        echo -e "${YELLOW}  test-ssh - Test SSH connectivity to both containers${NC}"
        echo -e "${YELLOW}  verify   - Show files in source and destination containers${NC}"
        exit 1
        ;;
esac
