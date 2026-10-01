#!/usr/bin/env bash
# Source MinIO setup - create 6 buckets and upload the test objects
set -euo pipefail

MINIO_ENDPOINT="http://localhost:9000"
MINIO_USER="minioadmin"
MINIO_PASS="minioadmin123"
ALIAS="local"
TMPDIR_DATA="/tmp/minio-testdata"

echo "[MinIO] Starting setup..."

# ── Wait for MinIO ────────────────────────────────────────────────────────────
MAX_RETRY=40
RETRY=0
until curl -sf "${MINIO_ENDPOINT}/minio/health/live" >/dev/null 2>&1; do
    RETRY=$((RETRY + 1))
    if [[ $RETRY -ge $MAX_RETRY ]]; then
        echo "[MinIO] ERROR: MinIO did not start within timeout."
        exit 1
    fi
    echo "[MinIO] Waiting for MinIO... (${RETRY}/${MAX_RETRY})"
    sleep 3
done
echo "[MinIO] MinIO is ready."

# ── Register the mc alias ─────────────────────────────────────────────────────
mc alias set "$ALIAS" "$MINIO_ENDPOINT" "$MINIO_USER" "$MINIO_PASS" >/dev/null
echo "[MinIO] mc alias configured."

# ── Create buckets ────────────────────────────────────────────────────────────
for bucket in raw-data processed-data images documents backups logs; do
    mc mb "${ALIAS}/${bucket}" 2>/dev/null && echo "[MinIO] Bucket created: $bucket" || echo "[MinIO] Bucket already exists: $bucket"
done

# ── Prepare the temporary test-file directory ─────────────────────────────────
rm -rf "$TMPDIR_DATA"
mkdir -p \
    "$TMPDIR_DATA/raw-data/sensors" \
    "$TMPDIR_DATA/raw-data/sales" \
    "$TMPDIR_DATA/processed-data/reports" \
    "$TMPDIR_DATA/processed-data/aggregated" \
    "$TMPDIR_DATA/images/products" \
    "$TMPDIR_DATA/images/banners" \
    "$TMPDIR_DATA/documents/contracts" \
    "$TMPDIR_DATA/documents/invoices" \
    "$TMPDIR_DATA/backups/daily" \
    "$TMPDIR_DATA/logs/app" \
    "$TMPDIR_DATA/logs/error"

# ── raw-data: CSV sensor data ─────────────────────────────────────────────────
for month in 2024-01 2024-02 2024-03; do
cat > "$TMPDIR_DATA/raw-data/sensors/sensor_${month}.csv" <<EOF
sensor_id,timestamp,temperature,humidity,pressure,location
S001,${month}-01T00:00:00Z,22.5,60.1,1013.2,Seoul-A
S001,${month}-01T01:00:00Z,22.1,61.3,1013.0,Seoul-A
S002,${month}-01T00:00:00Z,19.8,55.7,1012.5,Busan-B
S002,${month}-01T01:00:00Z,20.1,54.9,1012.8,Busan-B
S003,${month}-01T00:00:00Z,18.3,65.2,1014.1,Daegu-C
S003,${month}-01T01:00:00Z,18.7,64.8,1014.3,Daegu-C
S004,${month}-01T00:00:00Z,21.2,58.4,1013.6,Incheon-D
S004,${month}-01T01:00:00Z,21.5,57.9,1013.4,Incheon-D
EOF
done

# ── raw-data: CSV sales data ──────────────────────────────────────────────────
for quarter in Q1 Q2 Q3; do
cat > "$TMPDIR_DATA/raw-data/sales/sales_2024_${quarter}.csv" <<EOF
order_id,product_id,product_name,category,quantity,unit_price,total,customer_id,order_date,region
ORD-10001,P001,Galaxy S24 Ultra,Smartphone,1,1599000,1599000,C001,2024-01-15,Seoul
ORD-10002,P002,iPhone 15 Pro,Smartphone,2,1550000,3100000,C002,2024-01-16,Busan
ORD-10003,P003,MacBook Pro M3,Laptop,1,2490000,2490000,C003,2024-01-17,Daegu
ORD-10004,P004,LG Gram 17,Laptop,1,1690000,1690000,C004,2024-01-18,Incheon
ORD-10005,P005,Clean Code Book,Book,3,33000,99000,C005,2024-01-19,Gwangju
ORD-10006,P006,Go Programming,Book,2,38000,76000,C006,2024-01-20,Daejeon
ORD-10007,P007,Casual Shirt,Clothing,5,39000,195000,C007,2024-01-21,Seoul
ORD-10008,P008,Floral Dress,Clothing,2,59000,118000,C008,2024-01-22,Seoul
ORD-10009,P009,Organic Apple 5kg,Food,10,25000,250000,C001,2024-01-23,Seoul
ORD-10010,P010,Pixel 8 Pro,Smartphone,1,1199000,1199000,C003,2024-01-24,Daegu
EOF
done

# ── raw-data: JSON event stream ───────────────────────────────────────────────
cat > "$TMPDIR_DATA/raw-data/events_stream.json" <<'EOF'
{"event_id":"EVT-0001","type":"user_login","user_id":"U001","timestamp":"2024-01-01T09:00:00Z","ip":"192.168.1.10","device":"mobile"}
{"event_id":"EVT-0002","type":"product_view","user_id":"U001","timestamp":"2024-01-01T09:01:00Z","product_id":"P001","duration_sec":45}
{"event_id":"EVT-0003","type":"add_to_cart","user_id":"U001","timestamp":"2024-01-01T09:02:00Z","product_id":"P001","quantity":1}
{"event_id":"EVT-0004","type":"purchase","user_id":"U001","timestamp":"2024-01-01T09:05:00Z","order_id":"ORD-10001","amount":1599000}
{"event_id":"EVT-0005","type":"user_login","user_id":"U002","timestamp":"2024-01-01T10:00:00Z","ip":"10.0.0.55","device":"desktop"}
{"event_id":"EVT-0006","type":"search","user_id":"U002","timestamp":"2024-01-01T10:01:00Z","query":"laptop","results":12}
{"event_id":"EVT-0007","type":"product_view","user_id":"U002","timestamp":"2024-01-01T10:03:00Z","product_id":"P003","duration_sec":120}
{"event_id":"EVT-0008","type":"user_logout","user_id":"U001","timestamp":"2024-01-01T10:10:00Z"}
EOF

# ── raw-data: UTF-8 encoding verification object ──────────────────────────────
# Deliberately multibyte (Korean/Japanese/emoji) so a migration that mangles the
# object body or its charset is caught. Keep every other object ASCII-only.
cat > "$TMPDIR_DATA/raw-data/utf8_encoding_check.json" <<'EOF'
{"id":"UTF8-001","lang":"ko","text":"한글 인코딩 검증 문자열","bytes":33}
{"id":"UTF8-002","lang":"ja","text":"日本語エンコーディング検証","bytes":39}
{"id":"UTF8-003","lang":"emoji","text":"migration ok ✅ 🐛 🚀","bytes":26}
EOF

# ── processed-data: JSON aggregate reports ────────────────────────────────────
cat > "$TMPDIR_DATA/processed-data/reports/monthly_summary_2024_01.json" <<'EOF'
{
  "period": "2024-01",
  "generated_at": "2024-02-01T00:00:00Z",
  "sales": {
    "total_orders": 1250,
    "total_revenue": 1875000000,
    "avg_order_value": 1500000,
    "top_category": "Smartphone",
    "top_product": {"id": "P001", "name": "Galaxy S24 Ultra", "units": 320}
  },
  "customers": {
    "new_customers": 85,
    "returning_customers": 1165,
    "churn_rate": 0.032
  },
  "regions": [
    {"name": "Seoul",   "revenue": 750000000, "orders": 500},
    {"name": "Busan",   "revenue": 375000000, "orders": 250},
    {"name": "Daegu",   "revenue": 281250000, "orders": 188},
    {"name": "Incheon", "revenue": 187500000, "orders": 125},
    {"name": "Others",  "revenue": 281250000, "orders": 187}
  ]
}
EOF

cat > "$TMPDIR_DATA/processed-data/aggregated/product_daily_agg_2024_01_15.parquet.json" <<'EOF'
{
  "format": "parquet-snapshot",
  "date": "2024-01-15",
  "records": [
    {"product_id":"P001","views":1205,"carts":342,"purchases":89,"revenue":142311000},
    {"product_id":"P002","views":980, "carts":210,"purchases":61,"revenue":94550000},
    {"product_id":"P003","views":756, "carts":180,"purchases":45,"revenue":112050000},
    {"product_id":"P004","views":643, "carts":155,"purchases":38,"revenue":64220000},
    {"product_id":"P005","views":421, "carts":98, "purchases":72,"revenue":2376000}
  ]
}
EOF

# ── images: fake binary images (random bytes) ─────────────────────────────────
for name in product_p001 product_p002 product_p003 logo banner thumbnail; do
    openssl rand 51200 > "$TMPDIR_DATA/images/products/${name}.jpg"
done
for name in main_banner spring_sale event_2024; do
    openssl rand 102400 > "$TMPDIR_DATA/images/banners/${name}.png"
done

# ── documents: contracts and invoices ─────────────────────────────────────────
for i in 001 002 003; do
cat > "$TMPDIR_DATA/documents/contracts/contract_2024_${i}.txt" <<EOF
SERVICE AGREEMENT #2024-${i}
================================
Date: 2024-01-${i}0
Parties: Cloud Barista Inc. (Provider) and Client Corp ${i} (Client)

1. SCOPE OF SERVICE
   The Provider agrees to deliver cloud migration services as outlined in Schedule A.

2. TERM
   This agreement commences on 2024-01-${i}0 and continues for 12 months.

3. PAYMENT
   Client shall pay \$50,000 USD per month, due within 30 days of invoice.

4. CONFIDENTIALITY
   Both parties agree to maintain confidentiality of shared information.

Signed: ________________________  Date: 2024-01-${i}0
EOF
done

for i in 001 002 003 004 005; do
cat > "$TMPDIR_DATA/documents/invoices/invoice_2024_${i}.txt" <<EOF
INVOICE #INV-2024-${i}
========================
Issue Date : 2024-01-${i}0
Due Date   : 2024-02-${i}0
Bill To    : Client Corp ${i}
            123 Business St, Seoul, Korea

Services:
  Cloud Migration Consulting  : \$30,000
  Data Transfer (5TB)         : \$10,000
  Support & Maintenance       : \$10,000
                              ----------
  Total                       : \$50,000 USD

Payment Method: Bank Transfer
Account: 123-456-789 (Cloud Barista Bank)
EOF
done

# ── backups: fake backup archives ─────────────────────────────────────────────
for date in 2024-01-01 2024-01-08 2024-01-15 2024-01-22; do
    openssl rand 204800 > "$TMPDIR_DATA/backups/daily/db_backup_${date}.tar.gz"
done
openssl rand 512000 > "$TMPDIR_DATA/backups/full_backup_2024_01.tar.gz"

# ── logs: application logs ────────────────────────────────────────────────────
for month in 01 02 03; do
cat > "$TMPDIR_DATA/logs/app/app_2024_${month}.log" <<EOF
2024-${month}-01 00:00:01 INFO  [main] Application started. version=1.2.3
2024-${month}-01 00:00:02 INFO  [db] Database connection established. host=mariadb:3306
2024-${month}-01 00:00:03 INFO  [minio] MinIO connection established. endpoint=minio:9000
2024-${month}-01 09:00:01 INFO  [api] POST /api/connections 201 45ms user=admin
2024-${month}-01 09:00:15 INFO  [api] GET  /api/connections 200 12ms user=admin
2024-${month}-01 09:01:30 WARN  [migration] Migration M-001 slow: elapsed=5.2s
2024-${month}-01 09:05:00 INFO  [migration] Migration M-001 completed. files=1250 size=524MB
2024-${month}-01 12:00:00 INFO  [health] Health check OK
2024-${month}-01 18:30:22 ERROR [api] POST /api/migrations 500 - DB connection timeout
2024-${month}-01 18:30:23 INFO  [db] Reconnecting to database...
2024-${month}-01 18:30:25 INFO  [db] Reconnected successfully.
2024-${month}-01 23:59:59 INFO  [main] Daily stats: requests=8420 errors=3 avg_latency=18ms
EOF
done

cat > "$TMPDIR_DATA/logs/error/error_2024.log" <<'EOF'
2024-01-05 14:22:31 ERROR [ssh] SSH connection failed: host=192.168.1.100 err=connection refused
2024-01-05 14:22:34 ERROR [ssh] Retry 1/3 failed: host=192.168.1.100
2024-01-12 09:15:44 ERROR [minio] PutObject failed: bucket=raw-data key=large_file.csv err=context deadline exceeded
2024-01-18 03:30:12 ERROR [migration] Checksum mismatch: file=data.csv source_md5=abc123 dest_md5=xyz789
2024-01-25 16:45:09 ERROR [db] Query timeout: query=SELECT_LARGE_TABLE elapsed=30.1s
2024-02-03 11:20:55 ERROR [api] Authentication failed: user=unknown ip=203.0.113.42
2024-02-14 08:00:01 ERROR [scheduler] Event cleanup failed: err=table locked
EOF

# ── Upload to MinIO ───────────────────────────────────────────────────────────
echo "[MinIO] Uploading test objects..."

mc cp --recursive "$TMPDIR_DATA/raw-data/"       "${ALIAS}/raw-data/"
mc cp --recursive "$TMPDIR_DATA/processed-data/" "${ALIAS}/processed-data/"
mc cp --recursive "$TMPDIR_DATA/images/"         "${ALIAS}/images/"
mc cp --recursive "$TMPDIR_DATA/documents/"      "${ALIAS}/documents/"
mc cp --recursive "$TMPDIR_DATA/backups/"        "${ALIAS}/backups/"
mc cp --recursive "$TMPDIR_DATA/logs/"           "${ALIAS}/logs/"

echo "[MinIO] Bucket contents:"
for bucket in raw-data processed-data images documents backups logs; do
    count=$(mc ls --recursive "${ALIAS}/${bucket}" 2>/dev/null | wc -l)
    echo "  ${bucket}: ${count} objects"
done

# ── Clean up temporary files ──────────────────────────────────────────────────
rm -rf "$TMPDIR_DATA"

echo "[MinIO] Setup complete."
