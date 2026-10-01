#!/bin/bash

# Environment setup script for transx-ex inspect example
# Sets up three test environments:
#   - SSH container   (transx-inspect-ssh,   port 2222) with test directories
#   - MinIO container (transx-inspect-minio, port 9000) with test objects
#   - MySQL container (transx-inspect-mysql, port 3308) with test schema objects

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

SSH_CONTAINER="transx-inspect-ssh"
SSH_PORT=2222
SSH_KEY_DIR="${SCRIPT_DIR}/ssh_keys"
SSH_TEST_DATA_PATH="/data"
# Override with env var for remote host (e.g. SSH_HOST=192.168.1.10)
SSH_HOST="${SSH_HOST:-localhost}"

MINIO_CONTAINER="transx-inspect-minio"
MINIO_PORT=9000
MINIO_CONSOLE_PORT=9001
MINIO_ROOT_USER="minioadmin"
MINIO_ROOT_PASSWORD="minioadmin"
MINIO_BUCKET="inspect-test"
MINIO_ALIAS="inspect-minio"
# Override with env var for remote host (e.g. MINIO_HOST=192.168.1.20)
MINIO_HOST="${MINIO_HOST:-localhost}"

MYSQL_CONTAINER="transx-inspect-mysql"
MYSQL_IMAGE="mysql:latest"
# 3308, not 3306/3307 — the mysql-migration example already claims those.
MYSQL_PORT=3308
MYSQL_ROOT_PASSWORD="inspectpass"
MYSQL_DB="inspect_test"
# Override with env var for remote host (e.g. MYSQL_HOST=192.168.1.30)
MYSQL_HOST="${MYSQL_HOST:-localhost}"

echo -e "${BLUE}=========================================${NC}"
echo -e "${BLUE}  transx-ex inspect — Environment Setup  ${NC}"
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
}

# ── mc helper (no host mc dependency) ────────────────────────────────────────

# Run mc commands via Docker — no host mc installation required.
run_mc() {
    docker run --rm --network host minio/mc "$@"
}

# ── SSH container ─────────────────────────────────────────────────────────────

generate_ssh_keys() {
    print_status "Generating SSH keypair..."
    mkdir -p "${SSH_KEY_DIR}"

    if [[ -f "${SSH_KEY_DIR}/id_rsa" ]]; then
        print_success "SSH keypair already exists"
        return
    fi

    ssh-keygen -t rsa -b 4096 -f "${SSH_KEY_DIR}/id_rsa" -N "" -C "transx-inspect-test" &>/dev/null
    chmod 600 "${SSH_KEY_DIR}/id_rsa"
    chmod 644 "${SSH_KEY_DIR}/id_rsa.pub"
    print_success "SSH keypair generated: ${SSH_KEY_DIR}/id_rsa"
}

start_ssh_container() {
    print_status "Building SSH container image..."

    # Inline Dockerfile: Ubuntu + openssh-server, root login via public key only
    docker build -t transx-inspect-ssh:latest - <<'DOCKERFILE'
FROM ubuntu:22.04
RUN apt-get update && apt-get install -y --no-install-recommends openssh-server \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /var/run/sshd /root/.ssh \
    && chmod 700 /root/.ssh \
    && sed -i 's/#PermitRootLogin prohibit-password/PermitRootLogin yes/' /etc/ssh/sshd_config \
    && sed -i 's/#PubkeyAuthentication yes/PubkeyAuthentication yes/'    /etc/ssh/sshd_config \
    && sed -i 's/#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config
EXPOSE 22
CMD ["/usr/sbin/sshd", "-D"]
DOCKERFILE

    if docker ps -a --format '{{.Names}}' | grep -q "^${SSH_CONTAINER}$"; then
        print_warning "Removing existing container: ${SSH_CONTAINER}"
        docker rm -f "${SSH_CONTAINER}" &>/dev/null
    fi

    docker run -d --name "${SSH_CONTAINER}" \
        -p "${SSH_PORT}:22" \
        transx-inspect-ssh:latest

    print_status "Waiting for SSH daemon to be ready..."
    sleep 3

    # Install public key
    docker cp "${SSH_KEY_DIR}/id_rsa.pub" "${SSH_CONTAINER}:/root/.ssh/authorized_keys"
    docker exec "${SSH_CONTAINER}" chmod 600 /root/.ssh/authorized_keys
    print_success "SSH container is running (localhost:${SSH_PORT})"
}

create_ssh_test_data() {
    print_status "Creating test directory structure in SSH container..."

    docker exec "${SSH_CONTAINER}" bash -c "
        mkdir -p /data/project-alpha/src
        mkdir -p /data/project-alpha/docs
        mkdir -p /data/project-alpha/temp
        mkdir -p /data/project-beta/archive/2024
        mkdir -p /data/project-beta/logs
        mkdir -p /data/shared/config
        mkdir -p /data/tmp
        echo 'package main' > /data/project-alpha/src/main.go
        echo '# README' > /data/project-alpha/docs/README.md
        echo 'spec: v1' > /data/shared/config/app.yaml
        echo '[2024-01-01] started' > /data/project-beta/logs/app.log
        echo 'temp work' > /data/project-alpha/temp/work.txt
        echo 'scratch' > /data/tmp/scratch.txt
    "

    print_success "Test directories created:"
    echo "  /data/project-alpha/src"
    echo "  /data/project-alpha/docs"
    echo "  /data/project-alpha/temp     (exclude target)"
    echo "  /data/project-beta/archive/2024"
    echo "  /data/project-beta/logs      (exclude target)"
    echo "  /data/shared/config"
    echo "  /data/tmp                    (exclude target)"
}

test_ssh_connectivity() {
    print_status "Testing SSH connectivity (${SSH_HOST}:${SSH_PORT})..."

    if ssh -i "${SSH_KEY_DIR}/id_rsa" \
           -o StrictHostKeyChecking=no \
           -o ConnectTimeout=5 \
           -p "${SSH_PORT}" root@"${SSH_HOST}" \
           echo "SSH OK" &>/dev/null 2>&1; then
        print_success "SSH connection successful"
    else
        print_error "SSH connection failed"
        echo "  Try manually: ssh -i ${SSH_KEY_DIR}/id_rsa -p ${SSH_PORT} root@${SSH_HOST}"
    fi
}

# ── MinIO container ───────────────────────────────────────────────────────────

start_minio() {
    print_status "Starting MinIO container..."

    if docker ps -a --format '{{.Names}}' | grep -q "^${MINIO_CONTAINER}$"; then
        print_warning "Removing existing container: ${MINIO_CONTAINER}"
        docker rm -f "${MINIO_CONTAINER}" &>/dev/null
    fi

    docker run -d --name "${MINIO_CONTAINER}" \
        -p "${MINIO_PORT}:9000" \
        -p "${MINIO_CONSOLE_PORT}:9001" \
        -e MINIO_ROOT_USER="${MINIO_ROOT_USER}" \
        -e MINIO_ROOT_PASSWORD="${MINIO_ROOT_PASSWORD}" \
        minio/minio server /data --console-address ":9001"

    print_status "Waiting for MinIO to be ready..."
    local retries=20
    until run_mc alias set "${MINIO_ALIAS}" "http://localhost:${MINIO_PORT}" \
              "${MINIO_ROOT_USER}" "${MINIO_ROOT_PASSWORD}" &>/dev/null 2>&1; do
        sleep 2
        retries=$((retries - 1))
        if [[ $retries -le 0 ]]; then
            print_error "MinIO failed to start in time"
            exit 1
        fi
    done
    print_success "MinIO is running (localhost:${MINIO_PORT})"
}

upload_test_objects() {
    print_status "Creating bucket and uploading test objects..."

    # Run entirely inside the MinIO container — no extra containers or host mounts needed.
    docker exec "${MINIO_CONTAINER}" sh -c "
        mc alias set local http://localhost:9000 ${MINIO_ROOT_USER} ${MINIO_ROOT_PASSWORD} >/dev/null
        mc mb --ignore-existing local/${MINIO_BUCKET} >/dev/null
        mkdir -p /tmp/sample/logs /tmp/sample/archive /tmp/sample/tmp /tmp/sample/temp /tmp/sample/backup
        printf 'hello transx\n'             > /tmp/sample/hello.txt
        printf '{\"key\":\"value\"}\n'      > /tmp/sample/data.json
        printf 'id,name\n1,alice\n2,bob\n'  > /tmp/sample/users.csv
        printf '[2024-01-01] started\n'     > /tmp/sample/logs/app.log
        printf 'archived data\n'            > /tmp/sample/archive/old.tar.gz
        printf 'scratch\n'                  > /tmp/sample/tmp/scratch.txt
        printf 'temp work\n'                > /tmp/sample/temp/work.txt
        printf 'backup snapshot\n'          > /tmp/sample/backup/snapshot.tar.gz
        mc cp --recursive /tmp/sample/ local/${MINIO_BUCKET}/sample/ >/dev/null
    "

    print_success "Test objects uploaded to '${MINIO_BUCKET}/sample/':"
    echo "  sample/hello.txt"
    echo "  sample/data.json"
    echo "  sample/users.csv"
    echo "  sample/logs/app.log"
    echo "  sample/archive/old.tar.gz"
    echo "  sample/tmp/scratch.txt       (exclude target)"
    echo "  sample/temp/work.txt         (exclude target)"
    echo "  sample/backup/snapshot.tar.gz (exclude target)"
}

# ── MySQL container ───────────────────────────────────────────────────────────

start_mysql() {
    print_status "Starting MySQL container (${MYSQL_CONTAINER})..."

    if docker ps -a --format '{{.Names}}' | grep -q "^${MYSQL_CONTAINER}$"; then
        print_warning "Removing existing container: ${MYSQL_CONTAINER}"
        docker rm -f "${MYSQL_CONTAINER}" &>/dev/null
    fi

    docker run -d --name "${MYSQL_CONTAINER}" \
        -e MYSQL_ROOT_PASSWORD="${MYSQL_ROOT_PASSWORD}" \
        -e MYSQL_DATABASE="${MYSQL_DB}" \
        -p "${MYSQL_PORT}:3306" \
        "${MYSQL_IMAGE}" &>/dev/null

    wait_for_mysql
    print_success "MySQL is running (${MYSQL_HOST}:${MYSQL_PORT})"
}

wait_for_mysql() {
    print_status "Waiting for MySQL to accept connections..."
    for i in $(seq 1 40); do
        if docker exec "${MYSQL_CONTAINER}" \
            mysql -uroot -p"${MYSQL_ROOT_PASSWORD}" -e "SELECT 1" &>/dev/null; then
            print_success "MySQL ready (attempt ${i})"
            return 0
        fi
        sleep 3
    done
    print_error "MySQL did not become ready within 120s"
    exit 1
}

# Seed one table set plus every schema-object kind InspectDBMS can report for
# MySQL — views, functions, procedures, triggers, events — so the metric flags
# in config-mysql-metric.json have something to list.
create_mysql_test_data() {
    print_status "Seeding ${MYSQL_DB} with test objects..."

    # Events need the scheduler on; functions need the binlog trust flag.
    docker exec "${MYSQL_CONTAINER}" mysql -uroot -p"${MYSQL_ROOT_PASSWORD}" \
        -e "SET GLOBAL event_scheduler = ON; SET GLOBAL log_bin_trust_function_creators = 1;" \
        &>/dev/null || true

    docker exec -i "${MYSQL_CONTAINER}" \
        mysql -uroot -p"${MYSQL_ROOT_PASSWORD}" "${MYSQL_DB}" <<'SEED_SQL' &>/dev/null
-- Tables
CREATE TABLE users (
  id         INT AUTO_INCREMENT PRIMARY KEY,
  name       VARCHAR(100) NOT NULL,
  email      VARCHAR(100) UNIQUE NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE products (
  id         INT AUTO_INCREMENT PRIMARY KEY,
  name       VARCHAR(100) NOT NULL,
  price      DECIMAL(10,2) NOT NULL,
  stock      INT NOT NULL DEFAULT 0,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE orders (
  id         INT AUTO_INCREMENT PRIMARY KEY,
  user_id    INT NOT NULL,
  status     ENUM('pending','confirmed','shipped','cancelled') NOT NULL DEFAULT 'pending',
  ordered_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT fk_orders_user FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE order_items (
  id         INT AUTO_INCREMENT PRIMARY KEY,
  order_id   INT NOT NULL,
  product_id INT NOT NULL,
  qty        INT NOT NULL DEFAULT 1,
  unit_price DECIMAL(10,2) NOT NULL,
  CONSTRAINT fk_items_order   FOREIGN KEY (order_id)   REFERENCES orders(id),
  CONSTRAINT fk_items_product FOREIGN KEY (product_id) REFERENCES products(id)
);

CREATE TABLE audit_log (
  id         INT AUTO_INCREMENT PRIMARY KEY,
  event_type VARCHAR(50) NOT NULL,
  detail     TEXT,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Views
CREATE VIEW v_order_summary AS
  SELECT u.id AS user_id, u.name AS user_name,
         COUNT(DISTINCT o.id) AS order_count,
         IFNULL(SUM(oi.qty * oi.unit_price), 0) AS total_amount
    FROM users u
    LEFT JOIN orders o       ON u.id = o.user_id
    LEFT JOIN order_items oi ON o.id = oi.order_id
   GROUP BY u.id, u.name;

CREATE VIEW v_product_stock AS
  SELECT id, name, price, stock FROM products WHERE stock > 0;

-- Functions, procedures, triggers
DELIMITER //

CREATE FUNCTION fn_total_order_amount(p_user_id INT)
  RETURNS DECIMAL(12,2)
  READS SQL DATA
BEGIN
  DECLARE total DECIMAL(12,2) DEFAULT 0;
  SELECT IFNULL(SUM(oi.qty * oi.unit_price), 0) INTO total
    FROM orders o JOIN order_items oi ON o.id = oi.order_id
   WHERE o.user_id = p_user_id;
  RETURN total;
END//

CREATE FUNCTION fn_is_in_stock(p_product_id INT)
  RETURNS TINYINT(1)
  READS SQL DATA
BEGIN
  DECLARE cnt INT DEFAULT 0;
  SELECT stock INTO cnt FROM products WHERE id = p_product_id;
  RETURN cnt > 0;
END//

CREATE PROCEDURE sp_get_user_orders(IN p_user_id INT)
BEGIN
  SELECT o.id, o.status, o.ordered_at, SUM(oi.qty * oi.unit_price) AS total
    FROM orders o JOIN order_items oi ON o.id = oi.order_id
   WHERE o.user_id = p_user_id
   GROUP BY o.id, o.status, o.ordered_at;
END//

CREATE PROCEDURE sp_restock_product(IN p_id INT, IN p_qty INT)
BEGIN
  UPDATE products SET stock = stock + p_qty WHERE id = p_id;
  INSERT INTO audit_log (event_type, detail)
  VALUES ('restock', CONCAT('product_id=', p_id, ' qty=', p_qty));
END//

CREATE TRIGGER trg_orders_after_insert
  AFTER INSERT ON orders FOR EACH ROW
BEGIN
  INSERT INTO audit_log (event_type, detail)
  VALUES ('order_created', CONCAT('order_id=', NEW.id, ' user_id=', NEW.user_id));
END//

CREATE TRIGGER trg_stock_after_order
  AFTER INSERT ON order_items FOR EACH ROW
BEGIN
  UPDATE products SET stock = stock - NEW.qty WHERE id = NEW.product_id;
END//

DELIMITER ;

-- Events
CREATE EVENT IF NOT EXISTS evt_cleanup_old_logs
  ON SCHEDULE EVERY 1 DAY
  STARTS CURRENT_TIMESTAMP
  DO
    DELETE FROM audit_log WHERE created_at < DATE_SUB(NOW(), INTERVAL 30 DAY);

-- Rows (insert in FK order; the triggers fire on orders/order_items)
INSERT INTO users (name, email) VALUES
  ('Alice Kim',    'alice@example.com'),
  ('Bob Lee',      'bob@example.com'),
  ('Charlie Park', 'charlie@example.com');

INSERT INTO products (name, price, stock) VALUES
  ('Laptop Pro',          1299.99,  50),
  ('Wireless Mouse',        29.99, 200),
  ('Mechanical Keyboard',   89.99, 100),
  ('4K Monitor',           399.99,  30);

INSERT INTO orders (user_id, status) VALUES
  (1, 'confirmed'),
  (2, 'shipped'),
  (3, 'pending');

INSERT INTO order_items (order_id, product_id, qty, unit_price) VALUES
  (1, 1, 1, 1299.99),
  (1, 2, 2,   29.99),
  (2, 3, 1,   89.99),
  (3, 4, 1,  399.99);
SEED_SQL

    print_success "Test objects created (5 tables, 2 views, 2 functions, 2 procedures, 2 triggers, 1 event)"
}

test_mysql_connectivity() {
    print_status "Testing MySQL connectivity (${MYSQL_HOST}:${MYSQL_PORT})..."

    if docker exec "${MYSQL_CONTAINER}" \
        mysql -uroot -p"${MYSQL_ROOT_PASSWORD}" "${MYSQL_DB}" \
        -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='${MYSQL_DB}'" &>/dev/null; then
        print_success "MySQL connection OK"
    else
        print_error "MySQL connection failed"
        echo "  Try manually: docker exec -it ${MYSQL_CONTAINER} mysql -uroot -p${MYSQL_ROOT_PASSWORD} ${MYSQL_DB}"
        return 1
    fi
}

# ── Config file generation ────────────────────────────────────────────────────

generate_configs() {
    print_status "Generating ready-to-use config files..."

    # config-fs.json — points to localhost container
    cat > "${SCRIPT_DIR}/config-fs.json" <<EOF
{
  "storageType": "filesystem",
  "path": "${SSH_TEST_DATA_PATH}",
  "filesystem": {
    "accessType": "ssh",
    "ssh": {
      "host": "${SSH_HOST}",
      "port": ${SSH_PORT},
      "username": "root",
      "privateKeyPath": "${SSH_KEY_DIR}/id_rsa"
    }
  },
  "filter": {
    "maxDepth": 0,
    "exclude": ["tmp", "temp", "logs"]
  }
}
EOF

    # config-minio.json — points to localhost MinIO
    cat > "${SCRIPT_DIR}/config-minio.json" <<EOF
{
  "storageType": "objectstorage",
  "path": "${MINIO_BUCKET}/sample/",
  "objectStorage": {
    "accessType": "minio",
    "minio": {
      "endpoint": "${MINIO_HOST}:${MINIO_PORT}",
      "accessKeyId": "${MINIO_ROOT_USER}",
      "secretAccessKey": "${MINIO_ROOT_PASSWORD}",
      "region": "us-east-1",
      "useSSL": false
    }
  },
  "filter": {
    "maxDepth": 0,
    "exclude": ["tmp", "temp", "backup"]
  }
}
EOF

    # config-fs-metric.json — rules pipeline + filesystem metric
    cat > "${SCRIPT_DIR}/config-fs-metric.json" <<EOF
{
  "storageType": "filesystem",
  "path": "${SSH_TEST_DATA_PATH}",
  "filesystem": {
    "accessType": "ssh",
    "ssh": {
      "host": "${SSH_HOST}",
      "port": ${SSH_PORT},
      "username": "root",
      "privateKeyPath": "${SSH_KEY_DIR}/id_rsa"
    }
  },
  "filter": {
    "maxDepth": 0,
    "rules": [
      { "action": "exclude", "type": "glob", "pattern": "tmp" },
      { "action": "exclude", "type": "glob", "pattern": "temp" },
      { "action": "exclude", "type": "glob", "pattern": "logs" }
    ]
  },
  "metric": {
    "filesystem": {
      "totalSize": true,
      "fileCount": true,
      "extensionCount": true,
      "folderFileCount": true,
      "folderFileSize": true
    }
  }
}
EOF

    # config-minio-metric.json — rules pipeline + object storage metric
    cat > "${SCRIPT_DIR}/config-minio-metric.json" <<EOF
{
  "storageType": "objectstorage",
  "path": "${MINIO_BUCKET}/sample/",
  "objectStorage": {
    "accessType": "minio",
    "minio": {
      "endpoint": "${MINIO_HOST}:${MINIO_PORT}",
      "accessKeyId": "${MINIO_ROOT_USER}",
      "secretAccessKey": "${MINIO_ROOT_PASSWORD}",
      "region": "us-east-1",
      "useSSL": false
    }
  },
  "filter": {
    "maxDepth": 0,
    "rules": [
      { "action": "exclude", "type": "glob", "pattern": "tmp" },
      { "action": "exclude", "type": "glob", "pattern": "temp" },
      { "action": "exclude", "type": "glob", "pattern": "backup" }
    ]
  },
  "metric": {
    "objectStorage": {
      "totalSize": true,
      "objectCount": true,
      "extensionCount": true,
      "prefixModTime": true,
      "prefixObjectCount": true,
      "prefixObjectSize": true
    }
  }
}
EOF

    # config-mysql.json — points to the local MySQL container
    cat > "${SCRIPT_DIR}/config-mysql.json" <<EOF
{
  "dbmsType": "mysql",
  "database": "${MYSQL_DB}",
  "accessType": "direct",
  "direct": {
    "host": "${MYSQL_HOST}",
    "port": ${MYSQL_PORT},
    "username": "root",
    "password": "${MYSQL_ROOT_PASSWORD}"
  }
}
EOF

    # config-mysql-metric.json — every MySQL metric enabled
    cat > "${SCRIPT_DIR}/config-mysql-metric.json" <<EOF
{
  "dbmsType": "mysql",
  "database": "${MYSQL_DB}",
  "accessType": "direct",
  "direct": {
    "host": "${MYSQL_HOST}",
    "port": ${MYSQL_PORT},
    "username": "root",
    "password": "${MYSQL_ROOT_PASSWORD}"
  },
  "metric": {
    "mysql": {
      "rowCountExact": true,
      "columns": true,
      "indexes": true,
      "foreignKeys": true,
      "views": true,
      "functions": true,
      "procedures": true,
      "triggers": true,
      "events": true
    }
  }
}
EOF

    print_success "Config files generated: config-fs.json, config-minio.json, config-mysql.json, config-fs-metric.json, config-minio-metric.json, config-mysql-metric.json"
}

# ── Cleanup ───────────────────────────────────────────────────────────────────

cleanup() {
    print_status "Cleaning up..."

    for name in "${SSH_CONTAINER}" "${MINIO_CONTAINER}" "${MYSQL_CONTAINER}"; do
        if docker ps -a --format '{{.Names}}' | grep -q "^${name}$"; then
            docker rm -f "${name}" &>/dev/null
            print_success "Container removed: ${name}"
        fi
    done

    if [[ -d "${SSH_KEY_DIR}" ]]; then
        rm -rf "${SSH_KEY_DIR}"
        print_success "SSH keys removed"
    fi

    for f in "${SCRIPT_DIR}/config-fs.json" "${SCRIPT_DIR}/config-minio.json" \
             "${SCRIPT_DIR}/config-mysql.json" "${SCRIPT_DIR}/config-fs-metric.json" \
             "${SCRIPT_DIR}/config-minio-metric.json" "${SCRIPT_DIR}/config-mysql-metric.json"; do
        [[ -f "$f" ]] && rm -f "$f" && print_success "Removed: $(basename $f)"
    done
}

# ── Status display ─────────────────────────────────────────────────────────────

display_status() {
    echo ""
    echo -e "${BLUE}=========================================${NC}"
    echo -e "${GREEN}Environment Setup Completed!${NC}"
    echo -e "${BLUE}=========================================${NC}"

    echo ""
    echo -e "${YELLOW}SSH Container:${NC}"
    echo -e "  Host        : ${SSH_HOST}:${SSH_PORT}"
    echo -e "  Username    : root"
    echo -e "  Private Key : ${SSH_KEY_DIR}/id_rsa"
    echo -e "  Test Path   : ${SSH_TEST_DATA_PATH}"

    echo ""
    echo -e "${YELLOW}MinIO Object Storage:${NC}"
    echo -e "  API         : http://${MINIO_HOST}:${MINIO_PORT}"
    echo -e "  Console     : http://${MINIO_HOST}:${MINIO_CONSOLE_PORT}"
    echo -e "  Access Key  : ${MINIO_ROOT_USER}"
    echo -e "  Secret Key  : ${MINIO_ROOT_PASSWORD}"
    echo -e "  Bucket      : ${MINIO_BUCKET}"

    echo ""
    echo -e "${YELLOW}MySQL Database:${NC}"
    echo -e "  Host        : ${MYSQL_HOST}:${MYSQL_PORT}"
    echo -e "  Username    : root"
    echo -e "  Password    : ${MYSQL_ROOT_PASSWORD}"
    echo -e "  Database    : ${MYSQL_DB}"

    echo ""
    echo -e "${YELLOW}Run inspect:${NC}"
    echo -e "  ${GREEN}./inspect.sh -c config-fs.json${NC}                 # simple filter, no metric"
    echo -e "  ${GREEN}./inspect.sh -c config-minio.json${NC}"
    echo -e "  ${GREEN}./inspect.sh -c config-mysql.json${NC}              # sizes and table list only"
    echo -e "  ${GREEN}./inspect.sh -c config-fs-metric.json${NC}          # rules pipeline + metric"
    echo -e "  ${GREEN}./inspect.sh -c config-minio-metric.json -f json${NC}"
    echo -e "  ${GREEN}./inspect.sh -c config-mysql-metric.json${NC}       # + schema objects, exact rows"
}

# ── Main ──────────────────────────────────────────────────────────────────────

case "${1:-all}" in
    "all")
        check_tools
        generate_ssh_keys
        start_ssh_container
        create_ssh_test_data
        test_ssh_connectivity
        start_minio
        upload_test_objects
        start_mysql
        create_mysql_test_data
        test_mysql_connectivity
        generate_configs
        display_status
        ;;
    "ssh")
        check_tools
        generate_ssh_keys
        start_ssh_container
        create_ssh_test_data
        test_ssh_connectivity
        ;;
    "minio")
        check_tools
        start_minio
        upload_test_objects
        ;;
    "mysql")
        check_tools
        start_mysql
        create_mysql_test_data
        test_mysql_connectivity
        ;;
    "cleanup")
        cleanup
        ;;
    "test-ssh")
        test_ssh_connectivity
        ;;
    "test-mysql")
        test_mysql_connectivity
        ;;
    *)
        echo -e "${RED}Usage: $0 [all|ssh|minio|mysql|cleanup|test-ssh|test-mysql]${NC}"
        echo -e "${YELLOW}  all        - Setup SSH + MinIO + MySQL containers with test data (default)${NC}"
        echo -e "${YELLOW}  ssh        - Setup only SSH container${NC}"
        echo -e "${YELLOW}  minio      - Setup only MinIO container${NC}"
        echo -e "${YELLOW}  mysql      - Setup only MySQL container${NC}"
        echo -e "${YELLOW}  cleanup    - Stop containers and remove SSH keys / generated configs${NC}"
        echo -e "${YELLOW}  test-ssh   - Test SSH connectivity to the container${NC}"
        echo -e "${YELLOW}  test-mysql - Test MySQL connectivity to the container${NC}"
        exit 1
        ;;
esac
