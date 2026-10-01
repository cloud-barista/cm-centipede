#!/usr/bin/env bash
# setup_environment.sh — Docker environment setup for MySQL direct-to-direct migration example
#
# "all" also writes config.json from the container settings below;
# "cleanup" removes it again.
#
# Usage:
#   ./setup_environment.sh [all|source|target|status|cleanup] [--src-version VER] [--dst-version VER]
#
# Examples:
#   ./setup_environment.sh all                               # latest → latest
#   ./setup_environment.sh all --src-version 5.7 --dst-version 8.0
#   ./setup_environment.sh all --src-version 8.0 --dst-version 8.4
#   ./setup_environment.sh status
#   ./setup_environment.sh cleanup

set -euo pipefail

# =============================================================================
# Color output
# =============================================================================
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

# =============================================================================
# Defaults
# =============================================================================
COMMAND="${1:-all}"
shift 2>/dev/null || true

SRC_VERSION="latest"
DST_VERSION="latest"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --src-version) SRC_VERSION="$2"; shift 2 ;;
    --dst-version) DST_VERSION="$2"; shift 2 ;;
    -h|--help)     show_usage; exit 0 ;;
    *) echo -e "${RED}Unknown option: $1${NC}"; exit 1 ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

SRC_IMAGE="mysql:${SRC_VERSION}"
DST_IMAGE="mysql:${DST_VERSION}"
SRC_CONTAINER="mysql_src"
DST_CONTAINER="mysql_dst"
SRC_PORT=3306
DST_PORT=3307
SRC_PASS="srcpass"
DST_PASS="dstpass"
SRC_DB="testdb_src"
DST_DB="testdb_dst"
# Override with env vars when the containers are not on this machine
# (e.g. SRC_HOST=192.168.1.10 ./setup_environment.sh all)
SRC_HOST="${SRC_HOST:-127.0.0.1}"
DST_HOST="${DST_HOST:-127.0.0.1}"

# =============================================================================
# Usage
# =============================================================================
show_usage() {
  echo -e "${BLUE}MySQL Migration Environment Setup${NC}"
  echo -e "${YELLOW}Usage:${NC}"
  echo -e "  $0 [command] [--src-version VER] [--dst-version VER]"
  echo -e ""
  echo -e "${YELLOW}Commands:${NC}"
  echo -e "  all      Start source + target containers, seed data, write config.json (default)"
  echo -e "  source   Start only the source container and seed data"
  echo -e "  target   Start only the target container (empty DB)"
  echo -e "  status   Show container info, object counts, and row counts"
  echo -e "  cleanup  Stop and remove both containers and the generated config.json"
  echo -e ""
  echo -e "${YELLOW}Version options:${NC}"
  echo -e "  --src-version  MySQL version for source container (default: latest)"
  echo -e "  --dst-version  MySQL version for target container (default: latest)"
  echo -e ""
  echo -e "${YELLOW}Supported versions:${NC} 5.7  8.0  8.4  latest"
  echo -e ""
  echo -e "${YELLOW}Examples:${NC}"
  echo -e "  $0 all"
  echo -e "  $0 all --src-version 5.7 --dst-version 8.0"
  echo -e "  $0 all --src-version 8.0 --dst-version 8.4"
  echo -e "  $0 status"
  echo -e "  $0 cleanup"
}

# =============================================================================
# Prerequisite check
# =============================================================================
check_prerequisites() {
  echo -e "${CYAN}>> Checking prerequisites...${NC}"
  if ! command -v docker &>/dev/null; then
    echo -e "${RED}Docker not found. Install Docker and retry.${NC}"
    exit 1
  fi
  if ! docker info &>/dev/null; then
    echo -e "${RED}Docker daemon is not running. Start Docker and retry.${NC}"
    exit 1
  fi
  echo -e "${GREEN}  ✓ Docker is running${NC}"
}

# =============================================================================
# Wait for MySQL to be ready
# =============================================================================
wait_for_mysql() {
  local container="$1"
  local pass="$2"
  echo -e "${YELLOW}>> Waiting for MySQL in ${container} to be ready...${NC}"
  for i in $(seq 1 40); do
    if docker exec "${container}" mysql -uroot -p"${pass}" -e "SELECT 1" >/dev/null 2>&1; then
      echo -e "${GREEN}  ✓ MySQL ready (attempt ${i})${NC}"
      return 0
    fi
    sleep 3
  done
  echo -e "${RED}ERROR: MySQL in ${container} did not become ready within 120s${NC}"
  exit 1
}

# =============================================================================
# Start source container
# =============================================================================
start_source() {
  echo -e "${YELLOW}>> Starting source container (${SRC_IMAGE} → ${SRC_CONTAINER})...${NC}"
  docker rm -f "${SRC_CONTAINER}" 2>/dev/null || true
  docker run -d \
    --name "${SRC_CONTAINER}" \
    -e MYSQL_ROOT_PASSWORD="${SRC_PASS}" \
    -e MYSQL_DATABASE="${SRC_DB}" \
    -p "${SRC_PORT}:3306" \
    "${SRC_IMAGE}"
  echo -e "${GREEN}  ✓ ${SRC_CONTAINER} started (port ${SRC_PORT})${NC}"
}

# =============================================================================
# Start target container
# =============================================================================
start_target() {
  echo -e "${YELLOW}>> Starting target container (${DST_IMAGE} → ${DST_CONTAINER})...${NC}"
  docker rm -f "${DST_CONTAINER}" 2>/dev/null || true
  docker run -d \
    --name "${DST_CONTAINER}" \
    -e MYSQL_ROOT_PASSWORD="${DST_PASS}" \
    -e MYSQL_DATABASE="${DST_DB}" \
    -p "${DST_PORT}:3306" \
    "${DST_IMAGE}"
  echo -e "${GREEN}  ✓ ${DST_CONTAINER} started (port ${DST_PORT})${NC}"
}

# =============================================================================
# Seed source database
#
# Objects created:
#   Tables:     users, products, orders, order_items, audit_log
#   Views:      v_order_summary, v_product_stock
#   Functions:  fn_total_order_amount, fn_is_in_stock
#   Procedures: sp_get_user_orders, sp_restock_product
#   Triggers:   trg_orders_after_insert, trg_stock_after_order
#   Events:     evt_cleanup_old_logs
# =============================================================================
seed_source_db() {
  echo -e "${YELLOW}>> Seeding ${SRC_DB} with test fixtures...${NC}"

  # Enable event scheduler and allow function creation with binary log on
  docker exec "${SRC_CONTAINER}" mysql -uroot -p"${SRC_PASS}" \
    -e "SET GLOBAL event_scheduler = ON; SET GLOBAL log_bin_trust_function_creators = 1;" \
    2>/dev/null || true

  docker exec -i "${SRC_CONTAINER}" mysql -uroot -p"${SRC_PASS}" "${SRC_DB}" <<'SEED_SQL'
-- ===========================================================================
-- Tables
-- ===========================================================================
SET FOREIGN_KEY_CHECKS = 0;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS order_items;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS products;
DROP TABLE IF EXISTS users;
SET FOREIGN_KEY_CHECKS = 1;

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

-- ===========================================================================
-- Views
-- ===========================================================================
DROP VIEW IF EXISTS v_order_summary;
CREATE VIEW v_order_summary AS
  SELECT u.id   AS user_id,
         u.name AS user_name,
         COUNT(DISTINCT o.id)             AS order_count,
         IFNULL(SUM(oi.qty * oi.unit_price), 0) AS total_amount
    FROM users u
    LEFT JOIN orders o      ON u.id = o.user_id
    LEFT JOIN order_items oi ON o.id = oi.order_id
   GROUP BY u.id, u.name;

DROP VIEW IF EXISTS v_product_stock;
CREATE VIEW v_product_stock AS
  SELECT id, name, price, stock
    FROM products
   WHERE stock > 0;

-- ===========================================================================
-- Functions, Procedures, Triggers  (DELIMITER required)
-- ===========================================================================
DELIMITER //

DROP FUNCTION IF EXISTS fn_total_order_amount//
CREATE FUNCTION fn_total_order_amount(p_user_id INT)
  RETURNS DECIMAL(12,2)
  READS SQL DATA
BEGIN
  DECLARE total DECIMAL(12,2) DEFAULT 0;
  SELECT IFNULL(SUM(oi.qty * oi.unit_price), 0)
    INTO total
    FROM orders o
    JOIN order_items oi ON o.id = oi.order_id
   WHERE o.user_id = p_user_id;
  RETURN total;
END//

DROP FUNCTION IF EXISTS fn_is_in_stock//
CREATE FUNCTION fn_is_in_stock(p_product_id INT)
  RETURNS TINYINT(1)
  READS SQL DATA
BEGIN
  DECLARE cnt INT DEFAULT 0;
  SELECT stock INTO cnt FROM products WHERE id = p_product_id;
  RETURN cnt > 0;
END//

DROP PROCEDURE IF EXISTS sp_get_user_orders//
CREATE PROCEDURE sp_get_user_orders(IN p_user_id INT)
BEGIN
  SELECT o.id,
         o.status,
         o.ordered_at,
         SUM(oi.qty * oi.unit_price) AS total
    FROM orders o
    JOIN order_items oi ON o.id = oi.order_id
   WHERE o.user_id = p_user_id
   GROUP BY o.id, o.status, o.ordered_at;
END//

DROP PROCEDURE IF EXISTS sp_restock_product//
CREATE PROCEDURE sp_restock_product(IN p_id INT, IN p_qty INT)
BEGIN
  UPDATE products SET stock = stock + p_qty WHERE id = p_id;
  INSERT INTO audit_log (event_type, detail)
  VALUES ('restock', CONCAT('product_id=', p_id, ' qty=', p_qty));
END//

DROP TRIGGER IF EXISTS trg_orders_after_insert//
CREATE TRIGGER trg_orders_after_insert
  AFTER INSERT ON orders
  FOR EACH ROW
BEGIN
  INSERT INTO audit_log (event_type, detail)
  VALUES ('order_created', CONCAT('order_id=', NEW.id, ' user_id=', NEW.user_id));
END//

DROP TRIGGER IF EXISTS trg_stock_after_order//
CREATE TRIGGER trg_stock_after_order
  AFTER INSERT ON order_items
  FOR EACH ROW
BEGIN
  UPDATE products SET stock = stock - NEW.qty WHERE id = NEW.product_id;
END//

DELIMITER ;

-- ===========================================================================
-- Events
-- ===========================================================================
DROP EVENT IF EXISTS evt_cleanup_old_logs;
CREATE EVENT IF NOT EXISTS evt_cleanup_old_logs
  ON SCHEDULE EVERY 1 DAY
  STARTS CURRENT_TIMESTAMP
  DO
    DELETE FROM audit_log WHERE created_at < DATE_SUB(NOW(), INTERVAL 30 DAY);

-- ===========================================================================
-- Seed Data  (insert in FK order; triggers fire on orders/order_items)
-- ===========================================================================
INSERT INTO users (name, email) VALUES
  ('Alice Kim',     'alice@example.com'),
  ('Bob Lee',       'bob@example.com'),
  ('Charlie Park',  'charlie@example.com'),
  ('Diana Choi',    'diana@example.com'),
  ('Evan Oh',       'evan@example.com');

INSERT INTO products (name, price, stock) VALUES
  ('Laptop Pro',               1299.99, 50),
  ('Wireless Mouse',             29.99, 200),
  ('USB-C Hub',                  49.99, 150),
  ('Mechanical Keyboard',        89.99, 100),
  ('4K Monitor',                399.99,  30),
  ('Webcam HD',                  79.99,  80),
  ('Noise-Cancelling Headphones',199.99,  60),
  ('Standing Desk',             499.99,  20),
  ('Desk Lamp',                  39.99, 120),
  ('Cable Organizer',             9.99, 300);

-- trg_orders_after_insert fires here → 5 rows inserted into audit_log
INSERT INTO orders (user_id, status) VALUES
  (1, 'confirmed'),
  (2, 'shipped'),
  (3, 'pending'),
  (1, 'shipped'),
  (4, 'confirmed');

-- trg_stock_after_order fires here → products.stock decremented per order_item
INSERT INTO order_items (order_id, product_id, qty, unit_price) VALUES
  (1, 1,  1, 1299.99),
  (1, 2,  2,   29.99),
  (2, 4,  1,   89.99),
  (3, 5,  1,  399.99),
  (3, 6,  2,   79.99),
  (4, 3,  1,   49.99),
  (4, 7,  1,  199.99),
  (5, 8,  1,  499.99),
  (5, 9,  2,   39.99),
  (5, 10, 3,    9.99);
SEED_SQL

  echo -e "${GREEN}  ✓ Seed data loaded (Tables / Views / Functions / Procedures / Triggers / Events)${NC}"
}

# =============================================================================
# Config file generation
#
# One config for every scope: migrate.sh passes --scope, which overrides the
# scope recorded here.
# =============================================================================
generate_config() {
  echo -e "${YELLOW}>> Generating config file...${NC}"

  cat > "${SCRIPT_DIR}/config.json" <<EOF
{
  "source": {
    "dbmsType": "mysql",
    "database": "${SRC_DB}",
    "accessType": "direct",
    "direct": {
      "host": "${SRC_HOST}",
      "port": ${SRC_PORT},
      "username": "root",
      "password": "${SRC_PASS}"
    }
  },
  "destination": {
    "dbmsType": "mysql",
    "database": "${DST_DB}",
    "accessType": "direct",
    "direct": {
      "host": "${DST_HOST}",
      "port": ${DST_PORT},
      "username": "root",
      "password": "${DST_PASS}"
    }
  },
  "scope": "full",
  "rollbackOnFailure": true
}
EOF

  echo -e "${GREEN}  ✓ config.json${NC}"
}

# =============================================================================
# Show status
# =============================================================================
show_status() {
  echo -e "${BLUE}=========================================${NC}"
  echo -e "${BLUE} Environment Status${NC}"
  echo -e "${BLUE}=========================================${NC}"

  _show_container_status "${SRC_CONTAINER}" "${SRC_IMAGE}" "${SRC_PORT}" "${SRC_PASS}" "${SRC_DB}" "Source"
  echo ""
  _show_container_status "${DST_CONTAINER}" "${DST_IMAGE}" "${DST_PORT}" "${DST_PASS}" "${DST_DB}" "Target"
  echo ""
}

_show_container_status() {
  local container="$1" image="$2" port="$3" pass="$4" db="$5" label="$6"

  echo -e "${CYAN}[${label}] ${image}  →  localhost:${port} / ${db}${NC}"

  if ! docker ps -q -f "name=^${container}$" 2>/dev/null | grep -q .; then
    echo -e "  ${RED}Container not running${NC}"
    return
  fi

  # Object counts
  docker exec "${container}" mysql -uroot -p"${pass}" "${db}" --silent \
    -e "
SELECT CONCAT('  Tables:     ', COUNT(*)) FROM information_schema.TABLES
 WHERE TABLE_SCHEMA = '${db}' AND TABLE_TYPE = 'BASE TABLE';
SELECT CONCAT('  Views:      ', COUNT(*)) FROM information_schema.VIEWS
 WHERE TABLE_SCHEMA = '${db}';
SELECT CONCAT('  Procedures: ', COUNT(*)) FROM information_schema.ROUTINES
 WHERE ROUTINE_SCHEMA = '${db}' AND ROUTINE_TYPE = 'PROCEDURE';
SELECT CONCAT('  Functions:  ', COUNT(*)) FROM information_schema.ROUTINES
 WHERE ROUTINE_SCHEMA = '${db}' AND ROUTINE_TYPE = 'FUNCTION';
SELECT CONCAT('  Triggers:   ', COUNT(*)) FROM information_schema.TRIGGERS
 WHERE TRIGGER_SCHEMA = '${db}';
SELECT CONCAT('  Events:     ', COUNT(*)) FROM information_schema.EVENTS
 WHERE EVENT_SCHEMA = '${db}';
" 2>/dev/null || echo -e "  ${YELLOW}(DB not yet initialized)${NC}"

  # Row counts per table
  docker exec "${container}" mysql -uroot -p"${pass}" "${db}" --silent \
    -e "
SELECT CONCAT('  Rows: ',
  GROUP_CONCAT(CONCAT(TABLE_NAME,'=',TABLE_ROWS) ORDER BY TABLE_NAME SEPARATOR '  '))
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = '${db}' AND TABLE_TYPE = 'BASE TABLE';
" 2>/dev/null || true
}

# =============================================================================
# Cleanup
# =============================================================================
cleanup() {
  echo -e "${YELLOW}>> Removing containers...${NC}"
  docker rm -f "${SRC_CONTAINER}" "${DST_CONTAINER}" 2>/dev/null || true
  echo -e "${GREEN}  ✓ Containers removed${NC}"

  if [[ -f "${SCRIPT_DIR}/config.json" ]]; then
    rm -f "${SCRIPT_DIR}/config.json"
    echo -e "${GREEN}  ✓ Removed: config.json${NC}"
  fi
}

# =============================================================================
# Display final info
# =============================================================================
display_final_info() {
  echo ""
  echo -e "${BLUE}=========================================${NC}"
  echo -e "${GREEN} Environment Ready${NC}"
  echo -e "${BLUE}=========================================${NC}"
  echo ""
  echo -e "${YELLOW}Source container:${NC}"
  echo -e "  Image:    ${SRC_IMAGE}"
  echo -e "  MySQL:    ${SRC_HOST}:${SRC_PORT}  (root / ${SRC_PASS})"
  echo -e "  Database: ${SRC_DB}"
  echo ""
  echo -e "${YELLOW}Target container:${NC}"
  echo -e "  Image:    ${DST_IMAGE}"
  echo -e "  MySQL:    ${DST_HOST}:${DST_PORT}  (root / ${DST_PASS})"
  echo -e "  Database: ${DST_DB}  (empty, ready for restore)"
  echo ""
  echo -e "${YELLOW}Run migration:${NC}"
  echo -e "  ${GREEN}./migrate.sh full${NC}"
  echo -e "  ${GREEN}./migrate.sh schema-only${NC}"
  echo -e "  ${GREEN}./migrate.sh full --async --inspect${NC}"
  echo -e "  ${GREEN}./migrate.sh full --src-version ${SRC_VERSION} --dst-version ${DST_VERSION}${NC}"
}

# =============================================================================
# Main
# =============================================================================
check_prerequisites

case "${COMMAND}" in
  all)
    cleanup
    start_source
    start_target
    wait_for_mysql "${SRC_CONTAINER}" "${SRC_PASS}"
    wait_for_mysql "${DST_CONTAINER}" "${DST_PASS}"
    seed_source_db
    generate_config
    show_status
    display_final_info
    ;;
  source)
    docker rm -f "${SRC_CONTAINER}" 2>/dev/null || true
    start_source
    wait_for_mysql "${SRC_CONTAINER}" "${SRC_PASS}"
    seed_source_db
    show_status
    ;;
  target)
    docker rm -f "${DST_CONTAINER}" 2>/dev/null || true
    start_target
    wait_for_mysql "${DST_CONTAINER}" "${DST_PASS}"
    show_status
    ;;
  status)
    show_status
    ;;
  cleanup)
    cleanup
    ;;
  -h|--help)
    show_usage
    ;;
  *)
    echo -e "${RED}Unknown command: ${COMMAND}${NC}"
    show_usage
    exit 1
    ;;
esac
