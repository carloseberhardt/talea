#!/usr/bin/env bash
# Generates pds.env from pds.env.example with real secrets.
# Usage: ./scripts/generate-env.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
ENV_FILE="$PROJECT_DIR/pds.env"

if [ -f "$ENV_FILE" ]; then
  echo "pds.env already exists. Remove it first if you want to regenerate."
  exit 1
fi

# Generate PLC rotation key (secp256k1 private key)
if command -v openssl &> /dev/null; then
  PLC_KEY=$(openssl ecparam --name secp256k1 --genkey --noout --outform DER 2>/dev/null | tail -c +8 | head -c 32 | xxd -p -c 32)
else
  echo "Error: openssl is required to generate keys."
  exit 1
fi

# Generate JWT secret
JWT_SECRET=$(openssl rand -hex 32)

# Generate admin password
ADMIN_PASSWORD=$(openssl rand -hex 16)

cat > "$ENV_FILE" <<EOF
PDS_HOSTNAME=localhost
PDS_PORT=2583
PDS_DATA_DIRECTORY=/pds
PDS_DID_PLC_URL=http://plc:2582
PDS_JWT_SECRET=${JWT_SECRET}
PDS_ADMIN_PASSWORD=${ADMIN_PASSWORD}
PDS_PLC_ROTATION_KEY_K256_PRIVATE_KEY_HEX=${PLC_KEY}
PDS_SERVICE_HANDLE_DOMAINS=.localhost
PDS_INVITE_REQUIRED=false
PDS_BSKY_APP_VIEW_URL=
PDS_BSKY_APP_VIEW_DID=
PDS_REPORT_SERVICE_URL=
PDS_REPORT_SERVICE_DID=
PDS_MOD_SERVICE_URL=
PDS_MOD_SERVICE_DID=
PDS_CRAWLERS=
EOF

echo "Generated pds.env"
echo "Admin password: ${ADMIN_PASSWORD}"
echo "Save this — you'll need it for account creation."
