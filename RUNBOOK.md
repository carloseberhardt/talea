# Murmur Runbook

Step-by-step instructions to bring up the full stack from cold and validate the end-to-end loop.

## Prerequisites

- Docker and docker-compose
- Go 1.25+ (for building the relay, bridge, and CLI)
- Python 3.11+ and [`uv`](https://github.com/astral-sh/uv) (for the triage agent and any other Python A2A agents)
- mkcert (for local TLS certs)
- `/etc/hosts` entry: `127.0.0.1  pds.murmur.internal`
- mkcert CA installed: `mkcert -install` (one-time)
- The delivery agent from `~/projects/agent-skill-poc` (Python, uses `a2a-sdk >= 1.0.0a0`)
- WatsonX credentials (for triage's LLM calls): `WATSONX_API_KEY` and `WATSONX_PROJECT_ID`

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

Create a local `.env` file at the project root with the secrets AgentGateway needs to call LLM providers. **This file is gitignored — never commit real values.**

```bash
# .env
WATSONX_API_KEY=...
WATSONX_PROJECT_ID=...

# Optional: AWS credentials if you want the /bedrock route to work
AWS_ACCESS_KEY_ID=...
AWS_SECRET_ACCESS_KEY=...
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

## 3. Build the Bridge and CLI

```bash
cd ~/projects/murmur/bridge && go build -o bridge ./cmd/bridge/ && cd ..
cd ~/projects/murmur/cli    && go build -o murmur .            && cd ..
```

## 4. Start the Stack

```bash
docker compose up -d --build
```

The `--build` is needed because the AgentGateway image bakes in the local `agentgateway/config.yaml` at build time.

Verify everything is running:

```bash
docker compose ps
# Should show: plc-db (healthy), plc, pds, caddy, relay, agentgateway — all Up
```

Check services:

```bash
curl -s https://pds.murmur.internal/xrpc/_health        # PDS
curl -s http://localhost:2470/xrpc/_health               # Relay
curl -s -X POST http://localhost:4000/watsonx/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"openai/gpt-oss-120b","messages":[{"role":"user","content":"say hi"}]}' | jq
# AgentGateway → watsonx round-trip
```

## 5. Create Accounts (first time or after volume wipe)

```bash
./scripts/create-accounts.sh
```

This creates:
- `carlos.test` — human account
- `agent-one.test` — domain agent account (delivery agent)
- `triage-one.test` — swarm agent account (triage)
- `bridge.test` — bridge infrastructure account

If your `create-accounts.sh` predates triage, add `triage-one.test` to it (same shape as the other agent accounts).

## 6. Register PDS with Relay

```bash
./scripts/register-pds-with-relay.sh
```

## 7. Start the Delivery Agent

In a separate terminal:

```bash
cd ~/projects/agent-skill-poc
uv run python mock-agents/delivery_agent.py
# Serves A2A on port 5006
```

## 8. Start the Triage Agent

In a separate terminal:

```bash
cd ~/projects/murmur/agents/triage
uv sync             # first time only
uv run python triage_agent.py
# Serves A2A on port 5050
```

The startup log shows the bridge discovery URL, the LLM base URL, and the model name. If any of those are wrong for your environment, override with `BRIDGE_DISCOVERY_URL`, `LLM_BASE_URL`, or `LLM_MODEL`.

## 9. Start the Bridge

The bridge must be running before publishing agent profiles via the firehose path, but startup backfill (step 10) makes timing less critical than it used to be.

In a separate terminal:

```bash
cd ~/projects/murmur/bridge
env RELAY_URL=ws://localhost:2470 \
    PDS_URL=https://pds.murmur.internal \
    BRIDGE_HANDLE=bridge.test \
    BRIDGE_PASSWORD=bridge-sandbox-pass \
    TRIAGE_HANDLE=triage-one.test \
    ./bridge
```

Bridge env vars:

| Variable | Default | Purpose |
| --- | --- | --- |
| `RELAY_URL` | `ws://localhost:2470` | Relay firehose websocket |
| `PDS_URL` | `https://pds.murmur.internal` | PDS HTTP base |
| `BRIDGE_HANDLE` | `bridge.test` | Bridge's atproto handle |
| `BRIDGE_PASSWORD` | `bridge-sandbox-pass` | Bridge account password |
| `DISCOVERY_ADDR` | `:2471` | HTTP listen address for the discovery surface |
| `TRIAGE_HANDLE` | `triage.test` | Handle the bridge looks up to find the triage agent |

You should see:
- `authenticated as bridge` with the bridge DID
- `backfilling registry from PDS` followed by `registry backfill complete`
- `discovery server listening`
- `connected to firehose`

## 10. Publish Agent Profiles

With the bridge running, publish profiles for the delivery agent and the triage agent so they show up in the bridge's registry. `putRecord` with `rkey: "self"` is idempotent, so re-running is safe.

**Delivery agent (`agent-one.test`):**

```fish
set PWD "agent-one-sandbox-pass"

set TOKEN (curl -s -X POST https://pds.murmur.internal/xrpc/com.atproto.server.createSession \
  -H "Content-Type: application/json" \
  -d "{\"identifier\": \"agent-one.test\", \"password\": \"$PWD\"}" | jq -r '.accessJwt')

set DID (curl -s -X POST https://pds.murmur.internal/xrpc/com.atproto.server.createSession \
  -H "Content-Type: application/json" \
  -d "{\"identifier\": \"agent-one.test\", \"password\": \"$PWD\"}" | jq -r '.did')

set BODY (jq -n --arg did "$DID" '{
  repo: $did,
  collection: "community.swarm.agent.profile",
  rkey: "self",
  validate: false,
  record: {
    "$type": "community.swarm.agent.profile",
    name: "Delivery Agent",
    description: "Tracks package delivery status and answers shipping questions.",
    a2aEndpoint: "http://localhost:5006"
  }
}')

curl -s -X POST https://pds.murmur.internal/xrpc/com.atproto.repo.putRecord \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d "$BODY" | jq '.'
```

**Triage agent (`triage-one.test`):**

```fish
set PWD "triage-one-sandbox-pass"

set TOKEN (curl -s -X POST https://pds.murmur.internal/xrpc/com.atproto.server.createSession \
  -H "Content-Type: application/json" \
  -d "{\"identifier\": \"triage-one.test\", \"password\": \"$PWD\"}" | jq -r '.accessJwt')

set DID (curl -s -X POST https://pds.murmur.internal/xrpc/com.atproto.server.createSession \
  -H "Content-Type: application/json" \
  -d "{\"identifier\": \"triage-one.test\", \"password\": \"$PWD\"}" | jq -r '.did')

set BODY (jq -n --arg did "$DID" '{
  repo: $did,
  collection: "community.swarm.agent.profile",
  rkey: "self",
  validate: false,
  record: {
    "$type": "community.swarm.agent.profile",
    name: "Triage Agent",
    description: "Routes swarm signals to the right agents based on content.",
    a2aEndpoint: "http://localhost:5050"
  }
}')

curl -s -X POST https://pds.murmur.internal/xrpc/com.atproto.repo.putRecord \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d "$BODY" | jq '.'
```

In the bridge terminal you should see `discovered agent profile` followed by `agent registered` for each. Verify with the CLI:

```bash
./cli/murmur agents
```

Both `agent-one.test` and `triage-one.test` should appear.

**Note:** `putRecord` with identical content produces no commit (no firehose event). If something is off, change the `description` to trigger a new commit, or restart the bridge — startup backfill picks up profiles regardless.

## 11. Post a Signal as Carlos

The CLI is the easy way:

```bash
./cli/murmur post "where is package 241234"
```

That posts a tagless top-level signal. The bridge will see no mentions, no parent context, and call the triage agent to decide who should handle it.

To post a signal addressed directly to a specific agent (bypassing triage):

```bash
./cli/murmur post "@agent-one.test where is package 241234"
```

To reply within an existing thread:

```bash
./cli/murmur reply <parent-uri> "what about package 99"
```

## 12. Validate

**Bridge terminal** for a tagless signal should show:

```
signal received ... mentions=[]
calling triage ...
triage decision mentions=[agent-one.test] rationale=...
routing signal published ...
dispatching to agent ... handle=agent-one.test
A2A result received ... task_id=... context_id=...
reply signal published ...
```

**Read the thread back via the CLI:**

```bash
./cli/murmur thread <uri-from-post>
```

You should see the original request, a `(routing)` signal from `bridge.test` carrying triage's rationale, and a `(result)` signal with the agent's reply — both children of the request, properly indented.

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
- **AgentGateway won't start or crashes on config:** Check `docker compose logs agentgateway`. Make sure `.env` has `WATSONX_API_KEY` and `WATSONX_PROJECT_ID` set. The CEL transformation in `agentgateway/config.yaml` interpolates these into the request body — missing values will surface here.
- **Triage agent fails on `uv sync`:** Some `a2a-sdk` 1.0.0a transitive deps aren't declared as hard requirements. The triage `pyproject.toml` adds `sse-starlette`, `packaging`, and uses `a2a-sdk[sqlite]` and `uvicorn[standard]` to cover the gaps. If a new module surfaces, add it explicitly to dependencies.
- **Triage returns empty mentions for everything:** Check the bridge discovery URL is reachable from triage (`curl http://localhost:2471/agents`). Then check that the LLM is reachable through gateway. Then look at the LLM raw response in the triage logs — if the model is returning prose instead of JSON, the prompt may need tuning.
- **Relay rejects PDS registration:** The relay needs to reach `pds.murmur.internal` via Docker DNS. Check that Caddy has the network alias and the CA bundle is mounted.
- **Bridge can't authenticate:** Make sure the bridge account exists (`./scripts/create-accounts.sh`) and `PDS_URL` uses `https://pds.murmur.internal`.
- **Bridge sees a signal but doesn't route to anyone:** If `mentions=[]` and the bridge logs show "no mentions and triage agent not registered", the bridge can't find the triage agent in its registry. Check that the triage profile was published and that `TRIAGE_HANDLE` matches the triage account's handle.
- **A2A dispatch fails with "Method not found":** The agent's A2A SDK version doesn't match. The bridge uses the 1.0 spec (`SendMessage`). Ensure the agent uses `a2a-sdk >= 1.0.0a0` (Python) or equivalent.
- **A2A dispatch fails with unknown field errors:** The agent's SDK rejects fields like `kind`. This is a protobuf vs JSON format mismatch. The Go SDK (`a2a-go/v2`) and Python SDK (`a2a-sdk >= 1.0.0a0`) should agree — check both are on the latest versions.
- **PDS env changes don't take effect:** Use `docker compose up -d --force-recreate pds`, not `restart`.
