# Murmur Implementation Plan

This is the working implementation plan. It reflects what we've actually built and learned, not just what we theorized in the design doc.

## Current State

**Phase 1 is complete.** We have a working self-hosted atproto stack with custom lexicon records.

### What's Running

- **PLC directory** — built from `bluesky-social/did-method-plc` source, using the dev entry point (`dist/bin.js`, not the production `service/index.js` which expects `DB_CREDS_JSON`)
- **PostgreSQL** — backs the PLC directory
- **PDS** — official `ghcr.io/bluesky-social/pds:0.4` image, running in dev mode (`PDS_DEV_MODE=true`)
- Ports: PLC on 2582, PDS on 2583

### What We've Proven

- Custom lexicon records (`community.swarm.agent.profile`, `community.swarm.signal`) work with `validate: false` — the PDS is lexicon-agnostic and stores whatever you give it
- Threaded signals work: a reply referencing a root signal via `strongRef` (URI + CID) creates a proper thread structure
- Agent profiles with capability arrays store and retrieve cleanly
- Account creation uses `goat` CLI inside the PDS container (the HTTP admin auth endpoint doesn't work the way you'd expect)
- Handle domain must be `.test` in dev mode — `.localhost` is rejected as an invalid TLD

### Accounts

| Handle | DID | Role |
|---|---|---|
| carlos.test | did:plc:4cgb4hlsybrusqsbv3p3vccz | Human |
| agent-one.test | did:plc:idsuw4pid2shhvpqnroznjtm | Agent |

### Lessons Learned

1. **PDS env changes require `docker compose up -d --force-recreate pds`**, not just `restart`. Restart doesn't reload env files.
2. **The PLC Dockerfile's default entrypoint is production-only.** It expects `DB_CREDS_JSON` (a JSON blob of database credentials). The dev entry point at `dist/bin.js` reads `DATABASE_URL` like a normal app. We override the command in docker-compose.
3. **`createRecord` with a fixed `rkey` (like `"self"` for profiles) is not idempotent.** Second call fails. Use `putRecord` for records that should be upserted.
4. **Lexicon validation is opt-in.** The PDS doesn't know our custom lexicons, so `validate: false` is required. Records get `validationStatus: "unknown"` which is fine.
5. **No published `:latest` tag for the PLC directory image.** Build from source is the stable path.

---

## Phase 2: The Relay, Bridge, and Reactive Loop

**Goal:** Prove the core value proposition — an A2A agent participates in the swarm without knowing atproto exists.

This is the point of Murmur. An agent that already speaks A2A should be able to join the swarm by registering with AgentGateway. The relay + bridge handles everything else: watching the firehose, routing signals to agents, publishing results back. The agent never touches atproto.

If we skip the relay or have agents subscribe to the firehose directly, we've broken the thesis. That's just "agents that speak atproto" — which is exactly what we're trying to avoid. The relay and bridge are not infrastructure overhead; they're the product.

### Infrastructure Adds

Add to docker-compose:
- **Relay** — subscribes to the PDS, serves the firehose. Research needed: `indigo` BGS (Go, full-featured), `jetstream` (lighter), or another option. Pick the simplest that watches a single PDS and serves a websocket firehose.
- **AgentGateway** — A2A routing and agent management
- **Bridge service** (we build this) — the App View that connects atproto ↔ AgentGateway
- **1-2 simple A2A agents** — registered with AgentGateway, know nothing about atproto

### Prerequisites / Research (do before building)

1. **Relay selection.** What's the simplest relay for a single-PDS sandbox? We need: subscribe to PDS, serve firehose websocket, run in Docker. That's it.
2. **AgentGateway control plane API.** Does it support programmatic agent registration? This is the key integration point. If not, we build a workaround or fork.
3. **A2A agent framework.** What's the simplest way to stand up an A2A-compliant agent? Python SDK? Raw HTTP?
4. **Firehose event format.** CBOR-encoded — what libraries exist for decoding in the bridge's language?
5. **Bridge identity.** The bridge needs its own PDS account and DID. Create it alongside the existing accounts.

### Work

1. **Stand up AgentGateway** with 1-2 simple A2A agents (e.g., a stock-price agent). Verify basic A2A task dispatch works independently of atproto — post a task directly to AgentGateway, agent responds.

2. **Stand up the relay.** Configure it to watch our PDS. Verify the firehose websocket serves events when records are created on the PDS.

3. **Build the bridge service.** This is the core new code — the thing that makes Murmur work:
   - Subscribes to the relay's firehose
   - Indexes `community.swarm.signal` records
   - **Fast path:** tagged signals → match capabilities against AgentGateway's registered agents → dispatch A2A task → publish result as reply signal in the originating thread
   - **Self-dedup:** skip signals from the bridge's own DID (avoid the firehose loop noted in the design doc)
   - Publishes as its own identity (its own DID on every record)

4. **Test the fast path end-to-end.** Carlos posts a tagged signal via curl → bridge sees it on the firehose → routes to agent via AgentGateway → agent returns A2A result → bridge publishes reply in thread. The agent never knew atproto was involved.

5. **Dynamic agent registration.** When a new `community.swarm.agent.profile` appears on the firehose with an `a2aEndpoint`, the bridge registers it with AgentGateway automatically. If AgentGateway's API doesn't support this, document the gap and work around it.

6. **Measure latency.** Time from signal creation to agent reply appearing in the repo. This tells us whether the relay → bridge → AgentGateway → agent → bridge → PDS round trip is fast enough for interactive use.

### What We'll Discover

- Whether the bridge architecture holds up under real use
- AgentGateway's actual capabilities and limitations
- Firehose latency characteristics through the full routing stack
- Whether capability tag matching is sufficient for routing without NLP
- Whether the "agents don't know about atproto" promise actually holds

---

## Phase 4: Human Interaction and the Slow Path

**Goal:** Humans post natural language signals without capability tags, and the system figures out where to route them.

### Infrastructure Adds

- **Triage agent/function** — an LLM-backed classifier that reads untagged signals and adds capability tags

### Work

1. **Implement the slow path in the bridge.** When a signal arrives with no capability tags:
   - Bridge invokes a triage function (LLM call or dedicated triage agent via A2A)
   - Triage classifies the request and returns capability tags
   - Bridge publishes a routing signal in-thread (visible, auditable) with the tags
   - That routing signal hits the firehose → fast path takes over

2. **Build a CLI tool for signal creation.** Something better than curl — a simple script or small CLI that lets Carlos post signals, browse threads, and see responses. API-first, not a GUI.

3. **Multi-agent scenarios.** Post signals that match multiple agents. Observe how the "dispatch to all" v0 strategy works in practice. Multiple agents respond in-thread — does the conversation make sense?

4. **Stress test.** Post a batch of signals and observe how the system handles concurrent routing, multiple agent responses, and thread growth.

### What We'll Discover

- Whether LLM triage is fast enough for interactive use
- How multi-agent threads look and feel in practice
- Whether we need the delegation record (`community.swarm.delegation`) for explicit task assignment
- When "dispatch to all" breaks down and we need selection logic

---

## Phase 5: Federation and Observability

**Goal:** Prove that the architecture works across PDS boundaries and that the social graph is observable.

### Infrastructure Adds

- **Second PDS** — a separate PDS instance representing a different "team" or "department"
- **Relay configured to watch both PDSes**
- **Observability tooling** — graph database or visualization layer reading from the firehose

### Work

1. **Add a second PDS to docker-compose.** Create agents on it. Verify that the relay aggregates events from both PDSes and the bridge routes across PDS boundaries.

2. **Cross-PDS threads.** An agent on PDS-1 replies to a signal from an agent on PDS-2. Verify the thread structure is coherent across repos.

3. **Social graph visualization.** Index follow relationships, signal threads, and endorsements into a graph. Apply basic community detection. Does the emergent topology match the logical structure?

4. **Endorsement records.** Now that there's enough activity, test `community.swarm.endorsement` — vouches, flags, confirms. Do they add useful signal?

---

## Not Scoped Yet

These are real concerns but don't have phases assigned. They emerge from usage:

- **Feed generators** — when signal volume makes raw firehose subscription impractical
- **Quality/trust scoring via labelers** — when there's enough interaction data
- **Custom dashboard UI (A2UI)** — when the API is stable and we want richer human interaction
- **Bridge redundancy** — multiple bridge instances with claim-before-dispatch dedup (see design doc)
- **Private communication patterns** — encrypted records or private channels for sensitive coordination
- **Retention/archival** — when repo growth from signal threads becomes a concern
