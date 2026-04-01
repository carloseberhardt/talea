#!/usr/bin/env bash
# Creates the initial sandbox accounts: one human (carlos) and one agent.
# Requires: docker compose stack running.
# Usage: ./scripts/create-accounts.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

# Load admin password from pds.env
if [ ! -f "$PROJECT_DIR/pds.env" ]; then
  echo "Error: pds.env not found. Run generate-env.sh first."
  exit 1
fi
ADMIN_PASSWORD=$(grep PDS_ADMIN_PASSWORD "$PROJECT_DIR/pds.env" | cut -d= -f2)

# Wait for PDS to be ready
echo "Waiting for PDS..."
for i in $(seq 1 30); do
  if curl -sf "http://localhost:2583/xrpc/_health" > /dev/null 2>&1; then
    echo "PDS is ready."
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "Error: PDS did not become ready in time."
    exit 1
  fi
  sleep 1
done

create_account() {
  local handle="$1"
  local email="$2"
  local password="$3"

  echo "Creating account: $handle"
  docker compose exec pds goat pds admin account create \
    --pds-host http://localhost:2583 \
    --admin-password "$ADMIN_PASSWORD" \
    --handle "$handle" \
    --email "$email" \
    --password "$password"
  echo "  Created: $handle"
}

# Human account
create_account "carlos.test" "carlos@murmur.dev" "carlos-sandbox-pass"

# Agent account
create_account "agent-one.test" "agent-one@murmur.dev" "agent-one-sandbox-pass"

# Bridge account (infrastructure — publishes routing signals and results)
create_account "bridge.test" "bridge@murmur.dev" "bridge-sandbox-pass"

echo ""
echo "Accounts created."
echo ""
echo "To authenticate as carlos:"
echo "  curl -X POST http://localhost:2583/xrpc/com.atproto.server.createSession \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -d '{\"identifier\": \"carlos.test\", \"password\": \"carlos-sandbox-pass\"}'"
