#!/usr/bin/env bash
# Tells the relay to crawl our local PDS.
# Run once after the relay is up.
# Usage: ./scripts/register-pds-with-relay.sh

set -euo pipefail

RELAY_HOST="http://localhost:2470"
RELAY_ADMIN_PASSWORD="relay-admin-dev"
# Must match PDS_HOSTNAME — relay resolves this via Docker DNS
PDS_HOSTNAME="pds.talea.internal"

# Wait for relay to be ready
echo "Waiting for relay..."
for i in $(seq 1 30); do
  if curl -sf "$RELAY_HOST/xrpc/_health" > /dev/null 2>&1; then
    echo "Relay is ready."
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "Error: Relay did not become ready in time."
    exit 1
  fi
  sleep 1
done

echo "Requesting relay crawl of PDS at $PDS_HOSTNAME..."
response=$(curl -s -w "\n%{http_code}" -X POST "$RELAY_HOST/admin/pds/requestCrawl" \
  -H "Authorization: Basic $(echo -n "admin:${RELAY_ADMIN_PASSWORD}" | base64)" \
  -H "Content-Type: application/json" \
  -d "{\"hostname\": \"$PDS_HOSTNAME\"}")

http_code=$(echo "$response" | tail -1)
body=$(echo "$response" | head -n -1)

if [ "$http_code" -ge 200 ] && [ "$http_code" -lt 300 ]; then
  echo "PDS registered with relay."
else
  echo "Failed (HTTP $http_code): $body"
  exit 1
fi

echo ""
echo "Firehose available at: ws://localhost:2470/xrpc/com.atproto.sync.subscribeRepos"
echo ""
echo "To watch the firehose:"
echo "  docker compose exec pds goat firehose --relay-host ws://relay:2470"
