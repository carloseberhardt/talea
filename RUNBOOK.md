# Murmur Runbook

Step-by-step instructions to bring up the full stack from cold and validate the end-to-end loop.

## Prerequisites

- Docker and docker-compose
- Go 1.25+ (for building the relay and bridge)
- mkcert (for local TLS certs)
- `/etc/hosts` entry: `127.0.0.1  pds.murmur.internal`
- mkcert CA installed: `mkcert -install` (one-time)
- The delivery agent from `~/projects/agent-skill-poc` (Python, uses `a2a-sdk >= 1.0.0a0`)

## 1. Generate Secrets and Certs (first time only)

```bash
cd ~/projects/murmur

# Generate pds.env with secrets
./scripts/generate-env.sh

# Generate TLS certs
mkdir -p certs
mkcert -cert-file certs/pds.murmur.internal.pem \
       -key-file certs/pds.murmur.internal-key.pem \
       pds.murmur.internal

# Generate CA bundle (system CAs + mkcert CA, needed by relay)
./scripts/generate-ca-bundle.sh
```

## 2. Build the Relay (first time or after patching)

```bash
cd ~/projects/murmur

# Clone indigo if not already done
git clone --depth 1 https://github.com/bluesky-social/indigo.git relay-src

# Patch SSRF check for local sandbox
# In relay-src/util/ssrf/ssrf.go, replace PublicOnlyControl body with: return nil
# (and remove unused "fmt" import)

# Build
cd relay-src
go build -o ../relay/relay ./cmd/relay/
cd ..
```

## 3. Build the Bridge

```bash
cd ~/projects/murmur/bridge
go build -o bridge ./cmd/bridge/
cd ..
```

## 4. Start the Stack

```bash
docker compose up -d
```

Verify everything is running:

```bash
docker compose ps
# Should show: plc-db (healthy), plc, pds, caddy, relay — all Up
```

Check services:

```bash
curl -s https://pds.murmur.internal/xrpc/_health        # PDS
curl -s http://localhost:2470/xrpc/_health               # Relay
```

## 5. Create Accounts (first time or after volume wipe)

```bash
./scripts/create-accounts.sh
```

This creates:
- `carlos.test` — human account
- `agent-one.test` — agent account
- `bridge.test` — bridge infrastructure account

## 6. Register PDS with Relay

```bash
./scripts/register-pds-with-relay.sh
```

## 7. Start the Delivery Agent

In a separate terminal:

```bash
cd ~/projects/agent-skill-poc
uv run python mock-agents/delivery_agent.py
# Serves on port 5006
```

## 8. Start the Bridge

The bridge must be running before publishing agent profiles — it discovers agents by watching the firehose in real time.

In a separate terminal:

```bash
cd ~/projects/murmur/bridge
env RELAY_URL=ws://localhost:2470 \
    PDS_URL=https://pds.murmur.internal \
    BRIDGE_HANDLE=bridge.test \
    BRIDGE_PASSWORD=bridge-sandbox-pass \
    ./bridge
```

You should see:
- `authenticated as bridge` with the bridge DID
- `connected to firehose`

## 9. Publish Agent Profile

With the bridge running, publish a profile to register the delivery agent in the swarm. The bridge will discover it on the firehose, fetch the agent's A2A card to merge skill tags, and add it to its in-memory registry.

```bash
# Get auth token and DID for agent-one
set TOKEN (curl -s -X POST https://pds.murmur.internal/xrpc/com.atproto.server.createSession \
  -H "Content-Type: application/json" \
  -d '{"identifier": "agent-one.test", "password": "agent-one-sandbox-pass"}' | jq -r '.accessJwt')

set DID (curl -s -X POST https://pds.murmur.internal/xrpc/com.atproto.server.createSession \
  -H "Content-Type: application/json" \
  -d '{"identifier": "agent-one.test", "password": "agent-one-sandbox-pass"}' | jq -r '.did')

# Publish profile (putRecord = idempotent)
set BODY (jq -n --arg did "$DID" '{
  repo: $did,
  collection: "community.swarm.agent.profile",
  rkey: "self",
  validate: false,
  record: {
    "$type": "community.swarm.agent.profile",
    name: "Delivery Agent",
    description: "Tracks package delivery status and sends notifications.",
    capabilities: [
      {id: "delivery", name: "Delivery Tracking", description: "Track packages by number"},
      {id: "shipping", name: "Shipping Status", description: "Shipping and logistics queries"}
    ],
    a2aEndpoint: "http://localhost:5006"
  }
}')

curl -s -X POST https://pds.murmur.internal/xrpc/com.atproto.repo.putRecord \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d "$BODY" | jq '.'
```

In the bridge terminal you should see:
- `discovered agent profile` with the agent's DID and endpoint
- `agent tags resolved` with the merged tags from profile + agent card
- `agent registered`

**Note:** `putRecord` with identical content produces no commit (no firehose event). If the bridge missed the profile, change something in the record (e.g., the description) to trigger a new commit.

## 10. Post a Signal as Carlos

In a separate terminal:

```bash
set TOKEN (curl -s -X POST https://pds.murmur.internal/xrpc/com.atproto.server.createSession \
  -H "Content-Type: application/json" \
  -d '{"identifier": "carlos.test", "password": "carlos-sandbox-pass"}' | jq -r '.accessJwt')

set BODY (jq -n --arg ts (date -u +%Y-%m-%dT%H:%M:%S.000Z) '{
  repo: "carlos.test",
  collection: "community.swarm.signal",
  validate: false,
  record: {
    "$type": "community.swarm.signal",
    type: "request",
    content: "Where is my package 241234?",
    capabilities: ["delivery"],
    createdAt: $ts
  }
}')

curl -s -X POST https://pds.murmur.internal/xrpc/com.atproto.repo.createRecord \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d "$BODY" | jq '.'
```

## 11. Validate

**Bridge terminal** should show:
- `signal received` with the content and capabilities
- `dispatching to agent` with `dispatch_url=http://localhost:5006` (the agent's `a2aEndpoint`)
- `A2A result received` with the package status
- `reply signal published`

**Verify the reply is in the thread:**

```bash
curl -s "https://pds.murmur.internal/xrpc/com.atproto.repo.listRecords?repo=bridge.test&collection=community.swarm.signal&limit=5" \
  | jq '.records[] | {uri: .uri, type: .value.type, content: .value.content}'
```

Should show a `type: "result"` record with the package tracking info.

**Optional: watch the firehose in another terminal:**

```bash
docker compose exec pds goat firehose --relay-host ws://relay:2470
```

## Teardown

```bash
# Stop everything
docker compose down

# Stop and wipe all data (accounts, repos, relay state)
docker compose down -v
```

## Troubleshooting

- **PDS won't start:** Check `docker compose logs pds`. Common issues: missing `PDS_BLOBSTORE_DISK_LOCATION` or `PDS_DEV_MODE` in pds.env.
- **Relay rejects PDS registration:** The relay needs to reach `pds.murmur.internal` via Docker DNS. Check that Caddy has the network alias and the CA bundle is mounted.
- **Bridge can't authenticate:** Make sure the bridge account exists (`./scripts/create-accounts.sh`) and `PDS_URL` uses `https://pds.murmur.internal`.
- **Bridge doesn't see the agent profile:** The bridge only watches live firehose events. Publish the profile after the bridge is running. If re-publishing identical content, change something to trigger a new commit. (A startup PDS query that backfills the registry is on the Phase 3 to-do list.)
- **A2A dispatch fails with "Method not found":** The agent's A2A SDK version doesn't match. The bridge uses the 1.0 spec (`SendMessage`). Ensure the agent uses `a2a-sdk >= 1.0.0a0` (Python) or equivalent.
- **A2A dispatch fails with unknown field errors:** The agent's SDK rejects fields like `kind`. This is a protobuf vs JSON format mismatch. The Go SDK (`a2a-go/v2`) and Python SDK (`a2a-sdk >= 1.0.0a0`) should agree — check both are on the latest versions.
- **PDS env changes don't take effect:** Use `docker compose up -d --force-recreate pds`, not `restart`.
