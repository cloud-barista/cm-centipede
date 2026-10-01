#!/bin/bash

# Async File Migration Script for transx-ex
# Runs MigrateStorageAsync with real-time progress output and SIGINT cancellation.

set -e

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

Run async file migration using transx-ex (SSH → SSH via relay).

Options:
  -c, --config FILE    StorageMigrationModel configuration file (required)
  -v, --verbose        Enable verbose progress logging
  -h, --help           Show this help message

Available Configurations:
  template-config-ssh2ssh.json   SSH source → SSH destination (relay)

Examples:
  $0 -c config-ssh2ssh.json
  $0 -c config-ssh2ssh.json -v

EOF
}

CONFIG=""
VERBOSE=""

while [[ $# -gt 0 ]]; do
    case $1 in
        -c|--config)
            CONFIG="$2"
            shift 2
            ;;
        -v|--verbose)
            VERBOSE="-verbose"
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

if [[ -z "$CONFIG" ]]; then
    print_error "Configuration file is required. Use -c <config-file>"
    show_usage
    exit 1
fi

if [[ ! -f "$CONFIG" ]]; then
    print_error "Configuration file not found: $CONFIG"
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ ! -f "$SCRIPT_DIR/main" ]] || [[ "$SCRIPT_DIR/main.go" -nt "$SCRIPT_DIR/main" ]]; then
    print_status "Building migrate tool..."
    cd "$SCRIPT_DIR"
    go build -o main main.go
    print_success "Build complete"
fi

print_status "Starting migration with config: $CONFIG"
cd "$SCRIPT_DIR"

CMD_ARGS=("-config" "$CONFIG")
[[ -n "$VERBOSE" ]] && CMD_ARGS+=("$VERBOSE")

./main "${CMD_ARGS[@]}"
