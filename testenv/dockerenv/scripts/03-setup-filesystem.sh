#!/usr/bin/env bash
# Source filesystem setup - build the full /testdata file tree
set -euo pipefail

BASE="/testdata"
echo "[Filesystem] Creating test dataset under ${BASE}..."

# ── Create the directory structure ────────────────────────────────────────────
mkdir -p \
    "$BASE/documents/reports/annual" \
    "$BASE/documents/reports/quarterly" \
    "$BASE/documents/contracts" \
    "$BASE/documents/invoices/2024" \
    "$BASE/data/csv/sales" \
    "$BASE/data/csv/inventory" \
    "$BASE/data/json/configs" \
    "$BASE/data/json/metadata" \
    "$BASE/data/xml" \
    "$BASE/media/images" \
    "$BASE/media/videos" \
    "$BASE/media/audio" \
    "$BASE/logs/application/2024" \
    "$BASE/logs/system" \
    "$BASE/backups/database" \
    "$BASE/backups/config" \
    "$BASE/scripts/deploy" \
    "$BASE/scripts/maintenance"

# ── 1. Documents ─────────────────────────────────────────────────────────
for year in 2022 2023 2024; do
cat > "$BASE/documents/reports/annual/annual_report_${year}.txt" <<EOF
CLOUD BARISTA ANNUAL REPORT ${year}
====================================
Executive Summary
-----------------
Total Cloud Migrations: $(( year - 2020 ))50
Data Transferred: $(( year - 2019 ))00 TB
Customers Served: $(( (year - 2020) * 120 + 80 ))
Uptime SLA: 99.9%

Key Achievements
----------------
- Launched CM-Centipede v$(( year - 2020 )).0 multi-cloud migration platform
- Reduced average migration time by $(( year - 2019 ))5%
- Expanded to $(( year - 2019 )) new cloud providers
- ISO 27001 certification maintained

Financial Highlights
---------------------
Revenue    : \$$(( (year - 2020) * 5 + 10 ))M USD
Growth     : +$(( (year - 2020) * 8 + 20 ))% YoY
R&D Investment: 25% of revenue

Outlook for $(( year + 1 ))
----------------------------
Target: 500 enterprise customers
New Feature: AI-assisted migration planning
New Region: Southeast Asia expansion
EOF
done

for quarter in Q1 Q2 Q3 Q4; do
cat > "$BASE/documents/reports/quarterly/2024_${quarter}_summary.txt" <<EOF
2024 ${quarter} QUARTERLY SUMMARY
===================================
Period: 2024-${quarter}
Status: ${quarter} targets met

Migration Statistics:
  Filesystem Migrations : $(( RANDOM % 50 + 100 ))
  Object Storage Migr.  : $(( RANDOM % 30 + 80 ))
  DBMS Migrations       : $(( RANDOM % 20 + 40 ))
  Total Data Moved      : $(( RANDOM % 100 + 50 )) TB

Performance:
  Avg Migration Speed   : 850 MB/s
  Error Rate            : 0.02%
  Customer Satisfaction : 4.8/5.0
EOF
done

for i in $(seq 1 5); do
cat > "$BASE/documents/contracts/contract_2024_$(printf '%03d' $i).txt" <<EOF
SERVICE CONTRACT #2024-$(printf '%03d' $i)
Date: 2024-0${i}-10
Provider: Cloud Barista Inc.
Client: Enterprise Client $(printf '%03d' $i)

SERVICES: Multi-cloud data migration consulting and execution
TERM: 12 months from signing date
VALUE: \$$(( i * 10000 + 50000 )) USD
SLA: 99.9% uptime, 4-hour RTO, 1-hour RPO

Signed by Provider: ____________________
Signed by Client:   ____________________
EOF
done

for i in $(seq 1 8); do
    month=$(printf '%02d' $i)
cat > "$BASE/documents/invoices/2024/INV-2024-$(printf '%04d' $i).txt" <<EOF
INVOICE
Invoice #: INV-2024-$(printf '%04d' $i)
Date     : 2024-${month}-01
Due      : 2024-${month}-30

Cloud Migration Services - Month ${i}
  Professional Services (80h @ \$200/h) : \$16,000
  Data Transfer (10TB @ \$100/TB)       : \$1,000
  Platform License                       : \$3,000
                                        -----------
  Subtotal                               : \$20,000
  Tax (10%)                              : \$2,000
  Total                                  : \$22,000 USD
EOF
done

# ── 2. CSV data files ─────────────────────────────────────────────────────────
for quarter in Q1 Q2 Q3 Q4; do
{
echo "date,region,product_id,product_name,units_sold,revenue,cost,profit"
for i in $(seq 1 20); do
    echo "2024-01-$(printf '%02d' $i),Seoul,P$(printf '%03d' $i),Product $(printf '%03d' $i),$(( RANDOM % 100 + 10 )),$(( RANDOM % 1000000 + 100000 )),$(( RANDOM % 500000 + 50000 )),$(( RANDOM % 500000 + 50000 ))"
done
} > "$BASE/data/csv/sales/sales_2024_${quarter}.csv"
done

{
echo "product_id,product_name,category,sku,stock_qty,reorder_point,unit_cost,warehouse"
for i in $(seq 1 30); do
    echo "P$(printf '%03d' $i),Product $(printf '%03d' $i),Category-$(( i % 5 + 1 )),SKU-$(printf '%06d' $i),$(( RANDOM % 500 + 50 )),$(( RANDOM % 50 + 10 )),$(( RANDOM % 100000 + 10000 )),WH-$(( i % 3 + 1 ))"
done
} > "$BASE/data/csv/inventory/stock_snapshot_2024_01.csv"

{
echo "employee_id,name,department,position,hire_date,salary,location"
departments=("Engineering" "Sales" "Marketing" "HR" "Finance" "Operations")
positions=("Senior Engineer" "Manager" "Analyst" "Specialist" "Director" "Lead")
for i in $(seq 1 25); do
    dept=${departments[$((i % 6))]}
    pos=${positions[$((i % 6))]}
    echo "E$(printf '%04d' $i),Employee ${i},${dept},${pos},202$(( i % 4 ))-0$(( i % 9 + 1 ))-15,$(( RANDOM % 5000000 + 3000000 )),Seoul"
done
} > "$BASE/data/csv/inventory/employees_export.csv"

# ── 3. JSON config files ──────────────────────────────────────────────────────
cat > "$BASE/data/json/configs/app_config.json" <<'EOF'
{
  "application": {
    "name": "cm-centipede",
    "version": "2.0.0",
    "environment": "production"
  },
  "server": {
    "host": "0.0.0.0",
    "port": 8080,
    "timeout_sec": 30,
    "max_connections": 1000
  },
  "database": {
    "host": "mariadb",
    "port": 3306,
    "name": "centipede_db",
    "pool_size": 10,
    "max_idle": 5
  },
  "minio": {
    "endpoint": "minio:9000",
    "use_ssl": false,
    "bucket_prefix": "centipede"
  },
  "migration": {
    "max_parallel_workers": 4,
    "chunk_size_mb": 64,
    "retry_count": 3,
    "checksum_algorithm": "sha256"
  },
  "logging": {
    "level": "info",
    "format": "json",
    "output": "stdout"
  }
}
EOF

cat > "$BASE/data/json/configs/migration_profiles.json" <<'EOF'
{
  "profiles": [
    {
      "id": "profile-fs-basic",
      "name": "Basic Filesystem Migration",
      "type": "filesystem",
      "source": {"protocol": "sftp", "port": 22},
      "target": {"protocol": "sftp", "port": 22},
      "options": {"checksum": true, "preserve_permissions": true, "delete_source": false}
    },
    {
      "id": "profile-s3-standard",
      "name": "S3-Compatible Object Migration",
      "type": "objectstorage",
      "source": {"type": "minio", "port": 9000},
      "target": {"type": "aws-s3", "region": "ap-northeast-2"},
      "options": {"multipart_threshold_mb": 100, "storage_class": "STANDARD"}
    },
    {
      "id": "profile-mysql-full",
      "name": "Full MySQL/MariaDB Migration",
      "type": "dbms",
      "source": {"engine": "mariadb", "port": 3306},
      "target": {"engine": "mysql",   "port": 3306},
      "options": {"include_views": true, "include_routines": true, "include_triggers": true, "include_events": true}
    }
  ]
}
EOF

for i in 1 2 3; do
cat > "$BASE/data/json/metadata/dataset_meta_$(printf '%03d' $i).json" <<EOF
{
  "dataset_id": "DS-$(printf '%04d' $i)",
  "name": "Test Dataset $(printf '%03d' $i)",
  "created_at": "2024-0${i}-01T00:00:00Z",
  "updated_at": "2024-0${i}-15T12:00:00Z",
  "owner": "centipede-system",
  "tags": ["test", "migration", "dataset-${i}"],
  "schema_version": "1.$(( i - 1 ))",
  "record_count": $(( i * 10000 )),
  "size_bytes": $(( i * 1048576 )),
  "checksum": "sha256:$(openssl rand -hex 32)"
}
EOF
done

# ── 4. XML config files ───────────────────────────────────────────────────────
cat > "$BASE/data/xml/migration_config.xml" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<migration-config version="2.0">
  <source>
    <type>filesystem</type>
    <host>192.168.1.100</host>
    <port>22</port>
    <path>/data/source</path>
    <credentials encrypted="true">
      <username>migrate_user</username>
    </credentials>
  </source>
  <target>
    <type>filesystem</type>
    <host>192.168.2.100</host>
    <port>22</port>
    <path>/data/target</path>
  </target>
  <options>
    <parallel-workers>4</parallel-workers>
    <checksum-verify>true</checksum-verify>
    <retry-count>3</retry-count>
    <bandwidth-limit-mbps>100</bandwidth-limit-mbps>
  </options>
</migration-config>
EOF

# ── 5. Media files (fake binaries) ────────────────────────────────────────────
for name in product_001 product_002 product_003 logo_main banner_2024 thumbnail_default; do
    openssl rand 51200  > "$BASE/media/images/${name}.jpg"
done
for name in intro_video product_demo; do
    openssl rand 1048576 > "$BASE/media/videos/${name}.mp4"
done
for name in notification_sound alert_sound; do
    openssl rand 20480  > "$BASE/media/audio/${name}.mp3"
done

# ── 6. Log files ──────────────────────────────────────────────────────────────
for month in 01 02 03 04 05 06; do
{
echo "2024-${month}-01 00:00:00 INFO  [main] CM-Centipede v2.0.0 started"
echo "2024-${month}-01 00:00:01 INFO  [db] Connected to MariaDB: shop_db, hr_db"
echo "2024-${month}-01 00:00:02 INFO  [minio] Connected to MinIO: 6 buckets found"
for day in 01 10 20; do
echo "2024-${month}-${day} 09:00:00 INFO  [api] Migration job started: job_id=MIG-$(openssl rand -hex 4)"
echo "2024-${month}-${day} 09:30:00 INFO  [api] Migration completed: duration=30m transferred=15.2GB files=3420"
echo "2024-${month}-${day} 15:00:00 WARN  [scheduler] Slow query detected: table=inventory_log elapsed=2.1s"
done
echo "2024-${month}-28 23:59:59 INFO  [main] Monthly summary: jobs=90 success=89 failed=1"
} > "$BASE/logs/application/2024/app_2024_${month}.log"
done

cat > "$BASE/logs/system/syslog_2024.log" <<'EOF'
Jan 1 00:00:01 testenv kernel: Linux version 5.15.0 (Ubuntu 22.04)
Jan 1 00:00:05 testenv systemd[1]: Started cm-centipede test environment
Jan 1 00:00:10 testenv mariadbd[123]: ready for connections. port: 3306
Jan 1 00:00:15 testenv minio[456]: MinIO Object Storage Server started
Jan 2 03:00:01 testenv cron[789]: (root) CMD (/opt/testenv/scripts/cleanup.sh)
Jan 5 14:22:31 testenv kernel: EXT4-fs warning: mounting ext3 on loop0 is deprecated
Jan 15 09:15:00 testenv systemd[1]: Started migration job MIG-20240115
EOF

# ── 7. Deployment / maintenance scripts ───────────────────────────────────────
cat > "$BASE/scripts/deploy/deploy.sh" <<'EOF'
#!/usr/bin/env bash
# Deployment script for cm-centipede
set -euo pipefail

VERSION="${1:-latest}"
IMAGE="cloud-barista/cm-centipede:${VERSION}"

echo "Deploying ${IMAGE}..."
docker pull "$IMAGE"
docker stop cm-centipede 2>/dev/null || true
docker rm   cm-centipede 2>/dev/null || true
docker run -d --name cm-centipede -p 8080:8080 "$IMAGE"
echo "Deployment complete."
EOF

cat > "$BASE/scripts/maintenance/cleanup_logs.sh" <<'EOF'
#!/usr/bin/env bash
# Remove logs older than 30 days
find /var/log/cm-centipede -name "*.log" -mtime +30 -delete
echo "Log cleanup complete: $(date)"
EOF

# ── 8. UTF-8 encoding verification files ──────────────────────────────────────
# Every other file in the dataset is ASCII, and both the filename and the body
# here are deliberately multibyte (Korean, Japanese, emoji) so a migration that
# loses the encoding shows up as mojibake instead of passing silently.
cat > "$BASE/documents/한글_인코딩_검증.txt" <<'EOF'
한글 인코딩 검증 파일
가나다라마바사아자차카타파하
파일명과 본문 모두 UTF-8 멀티바이트입니다.
EOF

cat > "$BASE/documents/日本語_エンコード検証.txt" <<'EOF'
日本語エンコーディング検証ファイル
あいうえお かきくけこ さしすせそ
ファイル名と本文の両方が UTF-8 マルチバイトです。
EOF

cat > "$BASE/data/json/metadata/utf8_encoding_check.json" <<'EOF'
{
  "ko": "한글 인코딩 검증 문자열",
  "ja": "日本語エンコーディング検証",
  "emoji": "migration ok ✅ 🐛 🚀",
  "note": "All other files in this dataset are ASCII on purpose."
}
EOF

# ── Print file statistics ─────────────────────────────────────────────────────
total_files=$(find "$BASE" -type f | wc -l)
total_size=$(du -sh "$BASE" 2>/dev/null | cut -f1)
echo "[Filesystem] Created ${total_files} files, total size: ${total_size}"
echo "[Filesystem] Setup complete."
