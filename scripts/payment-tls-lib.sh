#!/usr/bin/env bash
# Sourced helpers for setup-payment-tls.sh. Do not execute directly.

log() {
  printf '[%s] %s\n' "$(date '+%H:%M:%S')" "$*" >&2
}

die() {
  log "ERROR: $*"
  exit 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

require_tools() {
  require_cmd aws
  require_cmd jq
}
