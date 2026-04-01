#!/usr/bin/env bash
# Creates a CA bundle combining system CAs with the mkcert local CA.
# The relay needs this to trust both public CAs and our local TLS certs.
# Usage: ./scripts/generate-ca-bundle.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
CERTS_DIR="$PROJECT_DIR/certs"
MKCERT_CA="$(mkcert -CAROOT)/rootCA.pem"

if [ ! -f "$MKCERT_CA" ]; then
  echo "Error: mkcert CA not found at $MKCERT_CA"
  echo "Run: mkcert -install"
  exit 1
fi

# Find system CA bundle
SYSTEM_CA=""
for path in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem; do
  if [ -f "$path" ]; then
    SYSTEM_CA="$path"
    break
  fi
done

if [ -z "$SYSTEM_CA" ]; then
  echo "Warning: system CA bundle not found, using only mkcert CA"
  cp "$MKCERT_CA" "$CERTS_DIR/ca-bundle.pem"
else
  cat "$SYSTEM_CA" "$MKCERT_CA" > "$CERTS_DIR/ca-bundle.pem"
fi

echo "CA bundle created at $CERTS_DIR/ca-bundle.pem"
