#!/bin/bash

# Inspect Script for transx-ex
# Lists directories (filesystem), objects (object storage) or schema metadata (DBMS)
# from a StorageLocation or DBMSLocation config.

set -e

# Color codes
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_status()  { echo -e "${BLUE}[INFO]${NC} $1"; }
print_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
print_error()   { echo -e "${RED}[ERROR]${NC} $1"; }

show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Inspect filesystem directories, object storage contents or DBMS schema metadata
using transx-ex.

Options:
  -c, --config FILE    StorageLocation / DBMSLocation configuration file (required)
  -f, --format FORMAT  Output format: table | json  (default: table)
  -v, --verbose        Enable verbose logging
  -n, --no-excluded    Skip the FILTERED block, which costs a second unfiltered scan
  -h, --help           Show this help message

Available Configurations:

  template-config-fs.json      Remote filesystem via SSH
  template-config-minio.json   MinIO / S3-compatible object storage
  template-config-mysql.json   MySQL database

Examples:
  # Inspect S3 bucket (JSON output)
  $0 -c template-config-minio.json -f json

  # Inspect remote server via SSH with verbose logging
  $0 -c template-config-fs.json -v

  # Inspect a MySQL database
  $0 -c template-config-mysql.json

EOF
}

# Defaults
CONFIG=""
FORMAT="table"
VERBOSE=""
EXCLUDED=""

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -c|--config)
            CONFIG="$2"
            shift 2
            ;;
        -f|--format)
            FORMAT="$2"
            shift 2
            ;;
        -v|--verbose)
            VERBOSE="-verbose"
            shift
            ;;
        -n|--no-excluded)
            EXCLUDED="-excluded=false"
            shift
            ;;
        -h|--help)
            show_usage
            exit 0
            ;;
        *)
            print_error "Unknown option: $1"
            show_usage
            exit 1
            ;;
    esac
done

# Validate
if [[ -z "$CONFIG" ]]; then
    print_error "Configuration file is required. Use -c <config-file>"
    show_usage
    exit 1
fi

if [[ ! -f "$CONFIG" ]]; then
    print_error "Configuration file not found: $CONFIG"
    exit 1
fi

if [[ "$FORMAT" != "table" && "$FORMAT" != "json" ]]; then
    print_error "Invalid format: $FORMAT (use: table, json)"
    exit 1
fi

# Build if needed
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ ! -f "$SCRIPT_DIR/main" ]] || [[ "$SCRIPT_DIR/main.go" -nt "$SCRIPT_DIR/main" ]]; then
    print_status "Building inspect tool..."
    cd "$SCRIPT_DIR"
    go build -o main main.go
    print_success "Build complete"
fi

# Run
print_status "Inspecting with config: $CONFIG"
cd "$SCRIPT_DIR"

CMD_ARGS=("-config" "$CONFIG" "-format" "$FORMAT")
[[ -n "$VERBOSE" ]] && CMD_ARGS+=("$VERBOSE")
[[ -n "$EXCLUDED" ]] && CMD_ARGS+=("$EXCLUDED")

./main "${CMD_ARGS[@]}"

print_success "Done"
