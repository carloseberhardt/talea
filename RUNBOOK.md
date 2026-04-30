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

Create a local `.env` file at the project root with secrets for AgentGateway and the bridge. **This file is gitignored — never commit real values.** The bridge auto-loads `.env` from its working directory at startup; AgentGateway picks it up via docker-compose's `env_file` directive.

```bash
# .env

# AgentGateway → LLM providers
WATSONX_API_KEY=...
WATSONX_PROJECT_ID=...

# Optional: AWS credentials if you want the /bedrock route to work
AWS_ACCESS_KEY_ID=...
AWS_SECRET_ACCESS_KEY=...

# Bridge: agents the bridge holds PDS credentials for. Format is a
# comma-separated list of `handle:password` pairs. The bridge opens a PDS
# session for each, and signs records on those agents' behalf when it
# publishes routing/result signals into a thread. Agents not listed here
# fall back to bridge authorship.
#
# (Sandbox passwords don't contain commas or colons; if you ever rotate to
# something that does, switch to bridge-managed credentials instead.)
MURMUR_AGENT_CREDS=agent-one.test:agent-one-sandbox-pass,weather-one.test:weather-one-sandbox-pass,orchestrator.test:orchestrator-sandbox-pass,threeaday-one.test:threeaday-one-sandbox-pass
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

## 3. Build the Bridge, CLI, and AppView

```bash
cd ~/projects/murmur/bridge  && go build -o bridge ./cmd/bridge/   && cd ..
cd ~/projects/murmur/cli     && go build -o murmur .               && cd ..
cd ~/projects/murmur/appview && go build -o appview ./cmd/appview/ && cd ..
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
- `weather-one.test` — domain agent account (weather)
- `orchestrator.test` — swarm agent account (orchestrator / general-purpose fallback)
- `threeaday-one.test` — domain agent account (threeaday CBT coach; lives in `~/projects/agents/threeaday`)
- `bridge.test` — bridge infrastructure account

## 6. Register PDS with Relay

```bash
./scripts/register-pds-with-relay.sh
```

## 7–8. Start All Agents

One script starts all agents (delivery, triage, weather, orchestrator, threeaday) in a single terminal. Ctrl-C kills them all cleanly:

```bash
./scripts/start-agents.sh
```

The delivery agent is expected at `~/projects/agent-skill-poc` by default; the swarm-native agents (triage, weather, orchestrator, threeaday) at `~/projects/agents/<agent>/`. Override with `AGENT_SKILL_POC_DIR` and `AGENTS_DIR` if yours live elsewhere. Each agent runs `uv sync` on startup so venvs are created/updated automatically.

Ports: delivery on 5006, triage on 5050, weather on 5051, orchestrator on 5052, threeaday on 5053.

If you prefer separate terminals for debugging, start each agent individually:

```bash
# Terminal 1: delivery
cd ~/projects/agent-skill-poc && uv run python mock-agents/delivery_agent.py

# Terminal 2: triage
cd ~/projects/agents/triage && uv sync && uv run python triage_agent.py

# Terminal 3: weather
cd ~/projects/agents/weather && uv sync && uv run python weather_agent.py

# Terminal 4: orchestrator
cd ~/projects/agents/orchestrator && uv sync && uv run python orchestrator_agent.py

# Terminal 5: threeaday
cd ~/projects/agents/threeaday && uv sync && uv run python threeaday_agent.py
```

The startup log shows the bridge discovery URL, the LLM base URL, and the model name. If any of those are wrong for your environment, override with `BRIDGE_DISCOVERY_URL`, `LLM_BASE_URL`, or `LLM_MODEL`.

## 9. Start the Bridge

The bridge must be running before publishing agent profiles via the firehose path, but startup backfill (step 10) makes timing less critical than it used to be.

In a separate terminal:

````bash
The bridge reads `.env` from its working directory at startup, so as long as you run it from the project root (or set `BRIDGE_ENV_FILE`), no environment-variable juggling is needed:

```bash
cd ~/projects/murmur
./bridge/bridge
````

Or if you prefer to run from `bridge/`, point at the project-root .env:

```bash
cd ~/projects/murmur/bridge
BRIDGE_ENV_FILE=../.env ./bridge
```

Bridge env vars (read from `.env` or the shell — shell wins):

| Variable              | Default                       | Purpose                                                                                                                                                                                                |
| --------------------- | ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `BRIDGE_ENV_FILE`     | `.env`                        | Path to the env file the bridge auto-loads at startup. Anything already set in the shell takes precedence over file values.                                                                            |
| `RELAY_URL`           | `ws://localhost:2470`         | Relay firehose websocket                                                                                                                                                                               |
| `PDS_URL`             | `https://pds.murmur.internal` | PDS HTTP base                                                                                                                                                                                          |
| `BRIDGE_HANDLE`       | `bridge.test`                 | Bridge's atproto handle                                                                                                                                                                                |
| `BRIDGE_PASSWORD`     | `bridge-sandbox-pass`         | Bridge account password                                                                                                                                                                                |
| `DISCOVERY_ADDR`      | `:2471`                       | HTTP listen address for the discovery surface                                                                                                                                                          |
| `MURMUR_AGENT_CREDS`  | _(unset)_                     | Comma-separated `handle:password` pairs. Each entry gives the bridge a PDS session it can use to sign records on that agent's behalf. Agents not listed fall back to bridge authorship with a warning. |
| `APPVIEW_URL`         | `http://localhost:2472`       | AppView base URL the bridge calls per-dispatch to fetch prior thread turns (Thread-Context A2A Extension v1). If unreachable, the bridge logs a warning and dispatches with `history: []` — strictly no worse than pre-extension behavior.                  |

You should see:

- `loaded env file` with the path
- `authenticated as bridge` with the bridge DID
- `authenticated as agent` lines for each entry in `MURMUR_AGENT_CREDS`
- `backfilling registry from PDS` followed by `registry backfill complete`
- `discovery server listening`
- `connected to firehose`

## 9b. Start the AppView

The AppView is the canonical thread indexer. It subscribes to the relay firehose, builds an in-memory thread index, and serves two consumers:

- **Bridge** — `GET /api/thread?root=<uri>` returns prior turns the bridge forwards to agents on every dispatch (Thread-Context A2A Extension v1). The bridge tolerates AppView outages (degrades to empty history); restart durability of agent conversations does depend on it being up.
- **Browser** — humans log in as their `MURMUR_HANDLE`, see the firehose live via WebSocket, post and reply, and watch agent results stream in.

In a separate terminal:

```bash
cd ~/projects/murmur
./appview/appview
```

AppView env vars (read from `.env` or the shell — shell wins):

| Variable               | Default                       | Purpose                                                                |
| ---------------------- | ----------------------------- | ---------------------------------------------------------------------- |
| `APPVIEW_ADDR`         | `:2472`                       | HTTP+WS listen address                                                 |
| `RELAY_URL`            | `ws://localhost:2470`         | Relay firehose websocket                                               |
| `PDS_URL`              | `https://pds.murmur.internal` | PDS HTTP base                                                          |
| `BRIDGE_DISCOVERY_URL` | `http://localhost:2471`       | Bridge agent registry — used to identify agent DIDs in the feed view   |
| `MURMUR_HANDLE`        | `carlos.test`                 | Human handle used for the AppView's "My Feed" filter and post/reply origin |
| `MURMUR_PASSWORD`      | `carlos-sandbox-pass`         | Password for the human account — required for posting from the web UI |

You should see:

- `loaded env file`
- `appview starting` with addr, relay, pds, and handle
- `appview listening`
- firehose subscriber connecting

Open the web UI at <http://localhost:2472>. The state is in-memory only — restarting the AppView clears the feed and it repopulates from new firehose events. (Startup backfill from PDS via `listRecords` is a known follow-up.)

## 10. Publish Agent Profiles

With the bridge running, publish each agent's `community.swarm.agent.profile` record so they show up in the bridge's registry. The fastest path is the checked-in manifest:

```bash
./cli/murmur publish-profile --file scripts/profiles.json
```

Expected output:

```
✓ agent-one.test         at://did:plc:.../community.swarm.agent.profile/self
✓ triage-one.test        at://did:plc:.../community.swarm.agent.profile/self
✓ weather-one.test       at://did:plc:.../community.swarm.agent.profile/self
✓ orchestrator.test      at://did:plc:.../community.swarm.agent.profile/self
✓ threeaday-one.test     at://did:plc:.../community.swarm.agent.profile/self
```

The manifest schema is one JSON object per agent with `handle`, `name`, `description`, `endpoint`, and an optional `password`. Either a single object or an array works. With no password set in the manifest or `MURMUR_PASSWORD` env, the CLI derives the sandbox password from the handle (`weather-one.test` → `weather-one-sandbox-pass`). `rkey="self"` makes the write idempotent, so re-running is safe.

For a single agent without a manifest:

```bash
./cli/murmur publish-profile \
  --handle weather-one.test \
  --name "Weather Agent" \
  --description "Answers questions about weather conditions, forecasts, and climate." \
  --endpoint http://localhost:5051
```

In the bridge terminal you should see `discovered agent profile` followed by `agent registered` for each. Verify:

```bash
./cli/murmur agents
```

All five agents should appear: `agent-one.test`, `triage-one.test`, `weather-one.test`, `orchestrator.test`, `threeaday-one.test`.

**Note:** `putRecord` with identical content produces no commit (no firehose event). If something is off, change the `description` to trigger a new commit, or restart the bridge — startup backfill picks up profiles regardless.

**Manual fallback:** if you need to inspect the wire shape (e.g., debugging a malformed record), the underlying call is `com.atproto.repo.putRecord` against the agent's PDS with body:

```json
{
  "repo": "<agent DID>",
  "collection": "community.swarm.agent.profile",
  "rkey": "self",
  "validate": false,
  "record": {
    "$type": "community.swarm.agent.profile",
    "name": "...",
    "description": "...",
    "a2aEndpoint": "..."
  }
}
```

## 11. Post a Signal as Carlos

The CLI is the easy way:

```bash
./cli/murmur post "where is package 241234"
```

That posts a tagless top-level signal. The bridge sees no mentions and takes no action; the triage agent, which subscribes to the firehose independently, picks it up, decides routing, and publishes its own routing signal. The bridge then sees the routing signal and dispatches to the named agents.

To post a signal addressed directly to a specific agent (bypassing triage):

```bash
./cli/murmur post "@agent-one.test where is package 241234"
```

To reply within an existing thread:

```bash
./cli/murmur reply <parent-uri> "what about package 99"
```

## 12. Validate

Three test cases exercise the three triage routing paths:

**Delivery routing:**

```bash
./cli/murmur post "where is package 241234"
./cli/murmur thread <uri-from-post>
# Expect: routing signal from triage → agent-one.test, result from agent-one.test
```

**Weather routing:**

```bash
./cli/murmur post "what's the weather like in Austin TX?"
./cli/murmur thread <uri-from-post>
# Expect: routing signal from triage → weather-one.test, result from weather-one.test
```

**Orchestrator fallback:**

```bash
./cli/murmur post "tell me a joke"
./cli/murmur thread <uri-from-post>
# Expect: routing signal from triage → orchestrator.test, result from orchestrator.test
```

**Ambiguity test (optional, observe):**

```bash
./cli/murmur post "will my package arrive before the rain stops?"
./cli/murmur thread <uri-from-post>
# Triage might pick both agents, or pick orchestrator. Either is valid.
```

All signals should be signed by the correct agent identity (visible in the thread output's `@handle` prefix). Any agent missing from `MURMUR_AGENT_CREDS` falls back to bridge authorship.

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
- **Triage returns empty mentions for everything:** The bridge will log ERROR and drop the signal (no orchestrator safety net). Check the bridge discovery URL is reachable from triage (`curl http://localhost:2471/agents`). Then check that the LLM is reachable through gateway. Then look at the LLM raw response in the triage logs — if the model is returning prose instead of JSON, the prompt may need tuning.
- **Relay rejects PDS registration:** The relay needs to reach `pds.murmur.internal` via Docker DNS. Check that Caddy has the network alias and the CA bundle is mounted.
- **Bridge can't authenticate:** Make sure the bridge account exists (`./scripts/create-accounts.sh`) and `PDS_URL` uses `https://pds.murmur.internal`.
- **Bridge sees a signal but doesn't route to anyone:** Tagless requests are now handled by triage via the firehose, not by a bridge-side synchronous call. If the bridge sees a `type=request` signal with `mentions=[]` and nothing happens afterward, check triage's own logs — it should log `◀ firehose request ...` followed by `▶ firehose decision: ...` and `published routing signal ...`. If those don't appear, triage isn't subscribed (check `authenticated to PDS` and `firehose subscriber starting` at triage startup). If they do appear but dispatch doesn't follow, the bridge isn't seeing the routing signal — verify the relay firehose is delivering by watching bridge logs for the routing signal's URI.
- **A2A dispatch fails with "Method not found":** The agent's A2A SDK version doesn't match. The bridge uses the 1.0 spec (`SendMessage`). Ensure the agent uses `a2a-sdk >= 1.0.0a0` (Python) or equivalent.
- **A2A dispatch fails with unknown field errors:** The agent's SDK rejects fields like `kind`. This is a protobuf vs JSON format mismatch. The Go SDK (`a2a-go/v2`) and Python SDK (`a2a-sdk >= 1.0.0a0`) should agree — check both are on the latest versions.
- **PDS env changes don't take effect:** Use `docker compose up -d --force-recreate pds`, not `restart`.
- **AppView shows an empty feed at startup:** Expected today — state is in-memory and the firehose only delivers new events. Post a new signal and it should appear in real time. Persistent backfill from PDS is a known follow-up.
- **Agent forgets a thread after restart:** The bridge fetches prior turns from the AppView per-dispatch; if the AppView is down the bridge logs a warning and sends `history: []`, so the agent legitimately has no context. Confirm `curl http://localhost:2472/api/thread?root=<rootURI>` returns the expected signals, then restart the AppView and try again.
