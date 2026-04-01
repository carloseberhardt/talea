#!/usr/bin/env bash
# End-to-end Phase 1 test: create sessions, publish agent profile,
# post a signal as carlos, reply as agent, read back the thread.
# Usage: ./scripts/sandbox-test.sh

set -euo pipefail

PDS_HOST="http://localhost:2583"

# --- Helper ---
pds_call() {
  local method="$1"
  local token="$2"
  local body="$3"
  curl -sf -X POST "$PDS_HOST/xrpc/$method" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $token" \
    -d "$body"
}

pds_get() {
  local method="$1"
  local params="$2"
  curl -sf "$PDS_HOST/xrpc/$method?$params"
}

echo "=== Murmur Phase 1 Sandbox Test ==="
echo ""

# --- Create sessions ---
echo "1. Creating sessions..."

carlos_session=$(curl -sf -X POST "$PDS_HOST/xrpc/com.atproto.server.createSession" \
  -H "Content-Type: application/json" \
  -d '{"identifier": "carlos.test", "password": "carlos-sandbox-pass"}')
carlos_token=$(echo "$carlos_session" | jq -r '.accessJwt')
carlos_did=$(echo "$carlos_session" | jq -r '.did')
echo "   Carlos DID: $carlos_did"

agent_session=$(curl -sf -X POST "$PDS_HOST/xrpc/com.atproto.server.createSession" \
  -H "Content-Type: application/json" \
  -d '{"identifier": "agent-one.test", "password": "agent-one-sandbox-pass"}')
agent_token=$(echo "$agent_session" | jq -r '.accessJwt')
agent_did=$(echo "$agent_session" | jq -r '.did')
echo "   Agent DID:  $agent_did"

# --- Publish agent profile ---
echo ""
echo "2. Publishing agent profile..."

profile_result=$(pds_call "com.atproto.repo.createRecord" "$agent_token" "{
  \"repo\": \"$agent_did\",
  \"collection\": \"community.swarm.agent.profile\",
  \"rkey\": \"self\",
  \"validate\": false,
  \"record\": {
    \"\$type\": \"community.swarm.agent.profile\",
    \"name\": \"Agent One\",
    \"description\": \"A general-purpose sandbox agent for testing the Murmur swarm.\",
    \"capabilities\": [
      {
        \"id\": \"financial-data\",
        \"name\": \"Financial Data\",
        \"description\": \"Can look up stock prices and financial metrics.\"
      },
      {
        \"id\": \"text-analysis\",
        \"name\": \"Text Analysis\",
        \"description\": \"Can perform sentiment analysis and text classification.\"
      }
    ],
    \"status\": \"available\"
  }
}")
echo "   Profile URI: $(echo "$profile_result" | jq -r '.uri')"

# --- Carlos posts a signal ---
echo ""
echo "3. Carlos posts a signal..."

now=$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")
signal_result=$(pds_call "com.atproto.repo.createRecord" "$carlos_token" "{
  \"repo\": \"$carlos_did\",
  \"collection\": \"community.swarm.signal\",
  \"validate\": false,
  \"record\": {
    \"\$type\": \"community.swarm.signal\",
    \"type\": \"request\",
    \"content\": \"Who can help me check the current stock price of AAPL?\",
    \"capabilities\": [],
    \"createdAt\": \"$now\"
  }
}")
signal_uri=$(echo "$signal_result" | jq -r '.uri')
signal_cid=$(echo "$signal_result" | jq -r '.cid')
echo "   Signal URI: $signal_uri"

# --- Agent replies ---
echo ""
echo "4. Agent replies in thread..."

now=$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")
reply_result=$(pds_call "com.atproto.repo.createRecord" "$agent_token" "{
  \"repo\": \"$agent_did\",
  \"collection\": \"community.swarm.signal\",
  \"validate\": false,
  \"record\": {
    \"\$type\": \"community.swarm.signal\",
    \"type\": \"result\",
    \"content\": \"AAPL: \$247.32 as of right now. Up 1.3% today.\",
    \"capabilities\": [\"financial-data\"],
    \"reply\": {
      \"root\": {\"uri\": \"$signal_uri\", \"cid\": \"$signal_cid\"},
      \"parent\": {\"uri\": \"$signal_uri\", \"cid\": \"$signal_cid\"}
    },
    \"createdAt\": \"$now\"
  }
}")
echo "   Reply URI: $(echo "$reply_result" | jq -r '.uri')"

# --- Read back records ---
echo ""
echo "5. Reading back records..."

echo ""
echo "   Agent profile:"
pds_get "com.atproto.repo.getRecord" "repo=$agent_did&collection=community.swarm.agent.profile&rkey=self" | jq '.value'

echo ""
echo "   Carlos's signals:"
pds_get "com.atproto.repo.listRecords" "repo=$carlos_did&collection=community.swarm.signal&limit=10" | jq '.records[] | {uri: .uri, type: .value.type, content: .value.content}'

echo ""
echo "   Agent's signals:"
pds_get "com.atproto.repo.listRecords" "repo=$agent_did&collection=community.swarm.signal&limit=10" | jq '.records[] | {uri: .uri, type: .value.type, content: .value.content, reply: .value.reply}'

echo ""
echo "=== Test complete ==="
