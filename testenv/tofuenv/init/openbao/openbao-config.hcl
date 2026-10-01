# OpenBao server configuration (file-based persistent storage)
# Data lives in a Docker volume (openbao-data), so it survives container restarts.
#
# Reference: https://openbao.org/docs/configuration/

# Persistent storage backend
storage "file" {
  path = "/openbao/data"
}

# TCP listener (local development - TLS disabled)
listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = true
}

# API address used for self-reference
api_addr = "http://0.0.0.0:8200"

# mlock is disabled for container compatibility (the IPC_LOCK capability covers it)
disable_mlock = true

# Enable the web UI
ui = true
