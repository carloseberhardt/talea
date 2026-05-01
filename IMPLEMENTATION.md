# Murmur Implementation Plan

This is the working implementation plan. It reflects what we've actually built and learned, not just what we theorized in the design doc.

## Current State

**Phases 1–4 (core path) are complete.** A human posts a natural-language signal. The triage agent, subscribed to the relay firehose directly, sees the signal, uses an LLM (via AgentGateway → watsonx) to decide which agents should handle it — choosing between multiple domain agents or falling back to the orchestrator for anything that doesn't fit a specialist — and publishes a routing signal under its own identity. The bridge sees that routing signal on the firehose, dispatches A2A to the mentioned agents, the agents reply (via LLM for weather and orchestrator, hardcoded for delivery), and the conversation can continue across multiple turns with per-agent context preserved — all without any A2A domain agent knowing atproto exists. Triage is the one agent that opts into being atproto-native. A separate AppView process indexes the firehose into in-memory threads and serves two consumers: the bridge (per-dispatch thread-history queries) and a real-time browser client where humans log in, post, reply, and watch the conversation. Conversational context is now durable across agent restarts: the bridge fetches each thread's prior turns from the AppView and forwards them in the A2A dispatch under a named extension, so agents reconstruct what happened from the signed atproto thread rather than from in-process memory.

### What's Running

- **PLC directory** — built from `bluesky-social/did-method-plc` source, using the dev entry point (`dist/bin.js`, not the production `service/index.js` which expects `DB_CREDS_JSON`)
- **PostgreSQL** — backs the PLC directory
- **PDS** — official `ghcr.io/bluesky-social/pds:0.4` image, dev mode, behind Caddy for TLS
- **Caddy** — TLS termination with mkcert certs for `pds.murmur.internal`
- **Relay** — built from `bluesky-social/indigo` source with SSRF check disabled for local sandbox. Uses SQLite, watches our PDS via the admin `requestCrawl` API.
- **Bridge** — Go service consuming the relay firehose, dispatching A2A tasks by @-mention, publishing results back as threaded reply signals. Event-driven: each `handleSignal` call does one thing per signal. Tagless requests (no mentions, no implicit continuation) are dropped by the bridge — the firehose-native triage picks them up and publishes a routing signal that the bridge then dispatches on. On every dispatch, the bridge calls the AppView's `/api/thread` to fetch the prior turns of the thread and forwards them inside the A2A message under the `https://swarm.community/extensions/thread-context/v1` extension, so agents can reconstruct conversation history even after a restart (decision #15). Exposes `POST /publish` so agents can initiate signals, plus `GET /agents` and `GET /healthz` on port 2471.
- **AppView** — Go service in `appview/cmd/appview/`. Subscribes to the relay firehose and maintains an in-memory thread index keyed by root URI; this is the canonical atproto-architectural role (the indexer that answers reverse queries the PDS can't, like "all signals under root X"). Two consumers today: the bridge calls `/api/thread?root=<uri>` per dispatch to populate the thread-context A2A extension, and a real-time browser client served from the same `embed.FS` lets humans log in (PDS app password), watch the firehose live over WebSocket, view threads, compose new signals, reply inline, and switch between a "My Feed" tab (threads where the user is author, participant, or mentioned) and a raw "Firehose" tab. Tracks the logged-in user via `/api/me`. AppView is currently soft-state: it has no persistence, so a restart starts with an empty index until the firehose delivers new events. PDS-backed startup backfill is on the deferred list.
- **AgentGateway** — back as an LLM router (port 4000). `/watsonx` → gpt-oss-120b, `/bedrock` → mistral-large. Used by the triage agent (and any future swarm-side service) so model choice is a config decision, not code. Never on the A2A dispatch path.
- **Triage agent** — Python service in `~/projects/agents/triage/`. The one agent that opts into being atproto-native: subscribes to the relay firehose directly, filters for tagless request signals (skipping self-authored, non-request types, and implicit-continuation signals whose parent has `a2a.agentDid`), runs the routing decision, and publishes a `routing` signal to its own PDS repo. Discovers available agents via the bridge's `/agents` endpoint, fetches each agent's A2A card directly, and asks an LLM via gateway which agents should handle the signal. Prompt instructs triage to always route somewhere — if no specialist fits, route to the orchestrator. For correlated multi-agent queries (where sub-queries reference each other), triage routes to the orchestrator instead of listing multiple specialists. Still exposes an A2A endpoint so external callers can use synchronous routing if desired; the murmur bridge no longer needs it.
- **Weather agent** — Python A2A service in `~/projects/agents/weather/`. LLM-backed domain agent that answers weather-related questions via gateway. Second domain agent, added so triage has real ambiguity to resolve.
- **Orchestrator agent** — Python A2A service in `~/projects/agents/orchestrator/`. LLM-backed orchestrator that both handles general queries directly and decomposes compound queries across specialist agents. When a query needs multiple specialists (especially correlated ones like "where's my package and what's the weather *there*?"), the orchestrator publishes signals via the bridge's `POST /publish` endpoint — each sub-dispatch is a routing signal with mentions and an `a2a` field pointing back to itself. The bridge dispatches to each specialist via the normal firehose path. When the specialist's reply arrives on the firehose, implicit continuation (rule #2) routes the result back to the orchestrator via A2A — no direct forwarding, no side channel. Each execute call is stateless; the orchestrator reconstructs conversation history from its task store. The whole cycle is visible in the atproto thread.
- **ThreeADay agent** — Python A2A service in `~/projects/agents/threeaday/`. CBT-informed behavioral-activation coach wrapped around the user's `threeaday` CLI. Responds to direct messages, and runs a background monitor that publishes proactive nudges via the bridge's `/publish` endpoint when daily progress is stalled during active hours.
- **Murmur CLI** — Go binary in `cli/`. `post`, `reply`, `thread`, `agents`, `whoami`. Replaces the curl ceremony for posting and reading signals.
- Ports: PLC on 2582, PDS on 2583 (HTTPS on 443 via Caddy), Relay on 2470, Bridge discovery on 2471, AppView on 2472, AgentGateway LLM on 4000, delivery agent on 5006, triage on 5050, weather on 5051, orchestrator on 5052, threeaday on 5053

### What We've Proven

- **The core thesis works.** A protocol-naive A2A agent (Python delivery-tracking agent) participates in the swarm without any knowledge of atproto. It receives tasks via standard A2A `SendMessage` and returns results. The bridge handles all atproto interaction.
- **@-mention dispatch works.** Signals carry a structured `mentions` field; the bridge resolves handles or DIDs to registered agents and dispatches by mention. Capability tags are gone — the agent card is the source of truth for what an agent does.
- **Multi-turn conversation memory works.** Each turn in a thread is its own A2A task, but tasks sharing a `contextId` are grouped. Agents reconstruct conversation history by querying their task store for all prior tasks with the same contextId and feeding the collected messages to the LLM. Verified end-to-end: ask about weather in Austin, follow up without naming the city, agent remembers. Key model insight: A2A tasks are units of work, not conversations. The thread is the conversation; `contextId` is the grouping key. The bridge sends only `contextId` on follow-ups (not `taskId`), so each turn gets a fresh task under the same context.
- **Implicit chat continuation works.** Replies in an agent thread don't need an explicit @ — the bridge resolves the parent's "responsible agent" (preferring the `a2a.agentDid` back-ref on result signals, falling back to the parent record's plain author DID for agent-initiated requests like a `threeaday` nudge), and if that DID is in the agent registry it's treated as the implicit mention with continuity. Analogous to Slack/Discord thread semantics. Triage's firehose filter applies the same widened rule, so user replies to agent-initiated requests don't get re-triaged on top of the bridge's dispatch.
- **LLM-based triage works and disambiguates.** Tagless top-level signals get routed to the right agent by the triage agent, which observes the firehose and publishes a routing signal under its own identity. With multiple domain agents registered (delivery + weather + threeaday), triage correctly picks the relevant specialist. Signals that don't fit any specialist route to the orchestrator. The triage decision is published as a `type: routing` signal in the thread for audit visibility.
- **The orchestrator fallback works.** Signals that don't match a specialist agent reach the orchestrator and get a useful response. If triage returns empty mentions, the bridge drops the signal with an ERROR log — the orchestrator fallback is triage's responsibility, not the bridge's.
- **Orchestrator decomposition works via `/publish`.** Compound queries with correlated sub-queries (e.g., "when does my package arrive and what's the weather there?") are routed to the orchestrator by triage. The orchestrator publishes sub-dispatch signals via `POST /publish`, each with an `a2a` field pointing back to itself. The bridge processes each signal through the normal firehose path, dispatches to the specialist, and forwards the result directly to the orchestrator. The orchestrator uses earlier results to formulate later queries (learns "Austin, TX" from delivery, asks weather about Austin). Each execute call is stateless — history is reconstructed from the task store. No bridge-side orchestrator loop, no bespoke dispatch API.
- **Threading is correct.** Bridge replies set `reply.root` to the actual thread root and `reply.parent` to the directly-replied-to message, so multi-turn conversations form a coherent tree.
- **Discovery without atproto knowledge works.** The bridge exposes its agent registry via plain HTTP, so the triage agent (and any future service) can find what's in the swarm without learning atproto.
- **`POST /publish` enables agent-initiated signals.** Any agent can publish a signal as its own identity by calling the bridge's `/publish` endpoint. The signal is committed to the PDS and flows through the firehose for normal processing. This is the missing "write" primitive that makes agents first-class participants — they can initiate, not just react. The orchestrator uses it for sub-dispatch; future agents (calendar reminders, monitoring alerts) will use it to speak first.
- **AppView is a real client and the canonical indexer.** Browser-based: log in with a PDS app password, the page subscribes to a WebSocket fed by the firehose, and threads update in place as agents reply. Compose a top-level signal, reply inline to any post in a thread, switch between My Feed (threads where the user is author / participant / mentioned) and Firehose (everything). The same in-memory thread index also serves the bridge over `/api/thread?root=<uri>`, so the AppView fills the atproto-architectural role of "the thing that answers reverse queries the PDS can't."
- **Agents survive restart mid-thread.** Implemented the Thread-Context A2A Extension v1 (URI `https://swarm.community/extensions/thread-context/v1`). On every dispatch, the bridge fetches prior turns from the AppView and includes them as a `DataPart` keyed by `community.swarm.threadContext`, declared in the extension via the `X-A2A-Extensions` HTTP header and `Message.extensions` field. Adopted by weather, threeaday, and orchestrator. Verified end-to-end: ask weather about a city, restart the weather process, follow up with "what about tomorrow?" — agent answers about the right city without re-asking. The atproto thread is now genuinely the source of truth for conversational state, not a parallel record. Bridge degrades gracefully if the AppView is unreachable: dispatches go out with empty history, behavior identical to pre-extension.
- **AppView is durable across its own restart.** At cold start the AppView reconstructs the in-memory thread index by walking `com.atproto.repo.listRecords` for every known repo (configured human handle + bridge-registered agents) and feeding the records through the same indexing path the firehose uses; backfilled signals dedup against firehose events by URI and skip the WebSocket broadcast so connected clients don't see reconstructed history as live activity. Verified against a non-trivial sandbox: 6 authors, 372 records pulled in ~48 ms, deduped to 88 threads. Restart durability of the thread-context extension no longer depends on whether the AppView happens to have been up since the thread started.
- **Bridge restart no longer drops in-flight signals.** Both bridge and AppView persist the highest firehose `seq` they've processed to a small file (`bridge.cursor`, `appview.cursor`) and resume the relay subscription with `?cursor=<seq>` on restart. Combined with triage's discovery cache (decision #16), an entire request → routing → dispatch → reply chain survives a brief bridge outage end-to-end: the request lands during downtime, triage routes against its cached agent list and publishes a routing signal anyway, and on bridge restart the firehose replays both events so dispatch happens after the fact.
- Custom lexicon records work with `validate: false`. Threaded signals via `strongRef` work as expected. The firehose delivers full record content. A2A protocol 1.0 PascalCase method names are required (`SendMessage`).

### Accounts

| Handle | Role |
|---|---|
| carlos.test | Human |
| agent-one.test | Domain agent (delivery tracker) |
| weather-one.test | Domain agent (weather, LLM-backed) |
| triage-one.test | Swarm agent (LLM-based router) |
| orchestrator.test | Swarm agent (general-purpose fallback, LLM-backed) |
| bridge.test | Bridge infrastructure (publishes routing + result signals) |

### Infrastructure Decisions Made

1. **PDS hostname is `pds.murmur.internal`** — Docker network alias + `/etc/hosts` entry. Required because DID documents bake in the PDS URL at account creation, and the relay needs to reach the PDS from inside Docker.
2. **Caddy for TLS** — mkcert local CA. The relay's SSRF protection and OAuth/client libraries are happier with real HTTPS. `mkcert -install` trusts the CA system-wide.
3. **Relay built from source** — The official Indigo relay has hardcoded SSRF protection that blocks connections to private IPs. We patched `util/ssrf/ssrf.go` to disable `PublicOnlyControl` for the sandbox. This is a one-line change in the Go source.
4. **Bridge dispatches directly to agents** — not through AgentGateway. AgentGateway integration was built and proven in commit `26d86e1` (its admin API on port 15000 supports runtime config updates via full-config replacement, not fine-grained CRUD), then removed from the dispatch path. The reason: putting the gateway in the dispatch path created a second registry — agent profiles in atproto repos *and* routes in AgentGateway — and made the bridge responsible for keeping the two in sync. Reconciliation would have made the bridge a worse SPOF without buying us anything the sandbox actually needs yet. The bridge now reads agent profiles from the firehose, keeps an in-memory registry, and dispatches directly to each agent's `a2aEndpoint`. AgentGateway can come back when we have a concrete need (observability of A2A traffic, policy enforcement, LLM proxy) that the bridge alone can't satisfy. The dispatch destination is a single per-call decision, so swapping it for a gateway URL later is a small change.
5. **Full results in reply signals** — the bridge publishes complete A2A results back to atproto threads, not just metadata. Network-level trust model for the sandbox (see design doc security section).
6. **AgentGateway is back, as an LLM router only.** Different role from the dispatch-path use we removed in commit `8ec5424`. Swarm-side agents (currently just triage) make their LLM calls through gateway on port 4000 so model selection becomes a config decision instead of code. `/watsonx` → gpt-oss-120b, `/bedrock` → mistral-large. Both expose OpenAI-compatible APIs. Gateway is *never* on the A2A dispatch path; the bridge still dispatches directly to each agent's `a2aEndpoint`. The two roles of gateway in this architecture (LLM router today, agent registration onramp later) are completely separate.

7. **Bridge exposes an HTTP surface** (port 2471, configurable via `DISCOVERY_ADDR`). `GET /agents` returns the current registry. `POST /publish` lets agents write signals to atproto as their own identity — the bridge uses the caller's credentials (which it already holds) to sign the record. `GET /healthz` for liveness. The boundary: A2A-native agents (orchestrator, weather, delivery, threeaday, and any contributor-written agent) never touch atproto — they call `/publish` and consume `/agents`. Only the bridge and atproto-native agents (currently just triage) speak atproto directly. See decision #8.

8. **Triage is atproto-native; every other agent is A2A-only.** Originally triage was a regular A2A agent: the bridge called it synchronously over `SendMessage`, got back a routing decision, and published the routing signal on triage's behalf. That shape worked but kept triage-specific special cases in the bridge (`TRIAGE_HANDLE` env var, sync A2A call, empty-mentions fallback to orchestrator). We flipped it: triage now subscribes to the relay firehose directly using the `atproto` SDK, and publishes its own routing signals via `com.atproto.repo.createRecord` against its PDS. In exchange the bridge becomes uniform — it just dispatches on any mention-bearing signal, regardless of who produced it. Why this inversion is okay given the "agents don't learn atproto" principle: that principle is about the *contributor adoption* story. Triage is infrastructure, built by the same people who operate the swarm. Asking it to speak atproto doesn't raise the barrier for external A2A agents. It also opens the door to running multiple triages in parallel as a live-eval harness, where the routing signals themselves become the log. The "swarm-agent runtime" concept (skills, event bus, framework) is still deferred entirely — triage got by with ~100 lines of subscription code, not a framework. A runtime would earn its keep only for cross-thread observers (summarization, dedup, correlation). When they exist, the runtime can come back — hosting only those, not per-signal classifiers.

9. **Routing decisions are published as `type: routing` signals and ARE the dispatch triggers.** Triage, watching the firehose, sees a tagless request, makes a decision, and publishes a `routing` signal under its own identity. Orchestrator sub-dispatches work the same way via `/publish`. In both cases, the routing signal arrives on the firehose and triggers dispatch in a separate `handleSignal` call. One signal, one action. No double-dispatch because routing signals aren't published and dispatched in the same call — they're published, then dispatched when they return. The bridge treats all routing signals uniformly regardless of author; it distinguishes triage-authored from orchestrator-authored by the *structural* presence of an `a2a` back-ref (orchestrator routing signals carry one; triage routing signals don't) — never by handle identity.

10. **The bridge signs records on behalf of agents whose credentials it holds.** Agent accounts on the PDS are part of the architecture — the bridge already has the right to hold their app passwords and publish under their identity. This makes the public thread a real signed audit trail: a reply signal authored by `agent-one.test` is the delivery agent's key signing its own response, not the bridge claiming on its behalf. The bridge holds a per-handle session map populated from `MURMUR_AGENT_CREDS` (a comma-separated list of `handle:password` pairs read out of `.env`). Reply signals and orchestrator-via-`/publish` signals route through `sessionFor(handle)`, which falls back to bridge authorship when no credentials are registered for the requested handle. Routing signals from triage are *not* in this map — triage holds its own PDS session and publishes directly (see decision #8); the bridge only sees those via the firehose. The firehose-side own-publisher check covers any DID the bridge publishes under, so dedup works regardless of authorship. **Deferred:** bridge-side account provisioning. The current env-var list is fine through ~10 agents but doesn't scale, and it pushes credential management onto operators. The next iteration is for the bridge to own account creation: one PDS admin secret enters the bridge, and it derives everything else (creates accounts, generates app passwords, persists them in its own state, publishes profile records). That collapses agent onboarding into a single bridge admin call and removes the runbook's manual curl dance entirely. It's the same shape as the gateway-as-onramp future direction (decision #4), but hosted in the bridge instead of in AgentGateway — same architectural move, smaller blast radius, doesn't bring gateway back into the dispatch path.

12. **Orchestrator decomposition uses the same routing pattern as triage — no bespoke dispatch API.** We initially built a `POST /dispatch` endpoint on the bridge so the orchestrator could sub-dispatch to specialist agents. This worked but was a side-channel. We then had the bridge drive the orchestrator loop (parsing dispatch instructions, sub-dispatching, feeding results back). This worked too but made the bridge stateful — both protocol adapter and orchestrator driver. The final architecture: the orchestrator publishes signals via `POST /publish` and drives its own loop. The bridge is event-driven and stateless per signal. See decision #13.

13. **Event-driven bridge + `/publish` primitive.** The bridge was doing too much in a single `handleSignal` call: triage, routing signal, dispatch, reply signal — all synchronously. `isOwnPublisher` existed to prevent double-dispatch from this design. The correct fix was type-based routing, not DID-based skipping. The bridge now applies two rules in order: (1) explicit mentions → dispatch; (2) type=`request`, no mentions, parent has `a2a.agentDid` → implicit continuation. Everything else drops. Tagless requests (type=`request`, no mentions, no implicit continuation) drop *on purpose* — triage, subscribed to the firehose independently, picks them up asynchronously and publishes a routing signal that re-enters via rule #1. There are no skips and no special-casing by DID. Routing signals carry mentions, so rule #1 handles them. Reply signals without mentions drop — they're audit records. **Rule #2 is restricted to `request` signals** because allowing result signals into rule #2 creates feedback loops: each reply signal published by the bridge has a parent whose `a2a` field points to some agent, which would trigger another dispatch. `POST /publish` is the write primitive that lets any agent initiate a signal. The orchestrator publishes sub-dispatch routing signals with `a2a.agentDid` pointing back to itself; the bridge detects this (structurally, via `a2a` field presence) and publishes the specialist reply with an explicit `mentions=[orchestrator]` entry, so the result re-enters via rule #1 (not rule #2). Every signal flows through the firehose uniformly. The firehose is the event bus.

11. **A2A tasks are units of work, not conversations.** Each turn in a thread creates and completes its own A2A task. Tasks sharing a `contextId` are grouped; agents may consult their task store as a fast path for prior context, but with the Thread-Context A2A Extension v1 (decision #15) the authoritative reconstruction comes from the prior turns the bridge forwards inside the dispatch — the same atproto thread the firehose published. The bridge sends only `contextId` on follow-ups (not `taskId`), so each turn gets a fresh task under the same context. Agent restart no longer starts a thread from scratch: the bridge re-sends the prior turns from atproto on the very next dispatch, so the in-memory task store is now a performance hint, not the only source of continuity. **Not yet handled:** the `input-required` task status, where a third-party agent keeps a task open across turns. That requires the bridge to send `taskId` (not just `contextId`) when resuming an open task — a separate slice.

14. **AppView is a separate process and serves two roles.** A Go service in `appview/cmd/appview/` subscribes to the relay firehose, builds an in-memory thread index keyed by root URI, and exposes both an HTTP API (`/api/thread`, `/api/me`, `/api/post`, plus a small login surface) and a real-time WebSocket. Architecturally this is the **canonical atproto thread indexer** — the role atproto AppViews exist to fill: PDSes are repo-shaped (no native reverse query by `reply.root`), so an AppView is the standard place those queries get answered. Two consumers today: a browser-based reference web client served from the same process (login, threads, compose, reply, My Feed vs. Firehose tabs), and the bridge, which calls `/api/thread` per dispatch to populate the thread-context A2A extension (decision #15). The fact that the AppView currently bundles its own UI is a deployment convenience — the bridge does not depend on or know about the UI surface; it depends on the indexer surface. Splitting the static-file serving into a separate web container would be cosmetic, not architectural. AppView is currently a soft dependency for the bridge: if it's down, dispatches still go out with empty history (decision #15), so the failure mode is "agents lose pre-restart context for that dispatch," not "swarm stops." Cold-start durability: at startup the AppView walks `com.atproto.repo.listRecords` for every known repo and replays the resulting signals through the same indexing path the firehose uses, so a fresh AppView has the full thread history before the firehose catches up — Phase 4 item #12.

15. **Thread-Context A2A Extension v1 makes restart durable.** A2A's `contextId` is only a grouping identifier; the spec doesn't guarantee agent-side persistence. We hit this when restarting the weather agent dropped its task store and it re-asked for a city the user had already given. The fix is to lean on what we already have: the signed atproto thread *is* the conversation. The bridge fetches the thread's prior turns from the AppView per dispatch, and forwards them inside the A2A `Message` as a `DataPart` keyed by `community.swarm.threadContext` (carrying signal/root URIs and CIDs, signal/root HTTP URLs, the recipient DID, and a list of history entries with author DID/handle, type, content, timestamp, and structural truncation markers). The extension is identified by URI `https://swarm.community/extensions/thread-context/v1`, declared on agent cards via `AgentCapabilities.extensions`, and activated per-dispatch via the `X-A2A-Extensions` HTTP header plus `Message.extensions`. Adopted by weather, threeaday, and orchestrator. Triage is exempt (stateless by design — it consumes the firehose record directly, not an A2A dispatch). Bridge holds zero thread state; on AppView failure it sends `history: []` and dispatch continues — strictly no worse than the pre-extension baseline. The architectural payoff: atproto stays authoritative, the AppView is the derived index, and agents reconstruct their view of a thread on every dispatch, so divergence between "what the agent thinks happened" and "what atproto says happened" becomes structurally impossible. Design doc at `/home/carlose/Downloads/thread-context-extension-design.md`. **Deferred:** real DID→PDS resolution (single-PDS sandbox today), pagination for long threads, signature verification by consuming agents, private/ACL threads, publishing the extension spec at a permanent w3id URI, and extracting a shared `agents/_shared/` helper module (forcing function reached at three adopters).

16. **Durability across short bridge / AppView downtimes is composed from three small mechanisms, not one big one.** The naive read is "containerize and let k8s restart the pod"; that solves auto-restart but doesn't solve the actual fragility, which is that events committed during a 10-second outage are past the firehose's live tail by the time the service reconnects. Three pieces close the gap end-to-end:

    - **Firehose cursor persistence.** Bridge and AppView each persist the highest seq observed to a file (paths configurable via `BRIDGE_CURSOR_PATH` / `APPVIEW_CURSOR_PATH`, defaults to `<service>.cursor` in cwd, gitignored as `*.cursor`). On startup the cursor is loaded and passed as the `?cursor=` query parameter to `com.atproto.sync.subscribeRepos`, so the relay replays everything between last-seen-seq and live tail. Cursor advances on every observed event, not just events we acted on — the file tracks "we got past this seq," not "we did meaningful work." Partial writes during a crash mid-fsync are detected by `loadCursor` and treated as "no cursor" (live tail), accepting at most one event of replay rather than failing loud.

    - **PDS session auto-refresh in both services.** A long-running server-side session inevitably hits token expiry. Bridge and AppView both detect expiry and either refresh via `com.atproto.server.refreshSession` or fall back to a full `createSession` with stored credentials. The detection is more permissive than 401-only: PDS uses HTTP 401 in some cases and HTTP 400 with body `{"error":"ExpiredToken"}` in others, so `isExpiredTokenError` checks both (also `InvalidToken` and `AuthRequired`). Concurrent callers dedupe via a stale-token check before re-fetching.

    - **Triage agent-list cache fallback.** Triage uses the bridge's `/agents` endpoint to enumerate routable agents at decision time. When the bridge is briefly unreachable, triage falls back to the last successfully-fetched list rather than emitting "drop, no fallback" — that drop verdict is otherwise terminal and there's no way to retroactively re-route an already-dropped signal. A startup warmup task (exponential backoff, capped at 30s) populates the cache before the first request, so even a "bridge was down when triage started" cold start eventually self-heals once the bridge comes up.

    What this combination does NOT cover: multi-instance HA (two bridges actively dispatching the same firehose would double-publish), persistent state beyond what's reconstructible from the PDS, or downtimes longer than the relay's retention window. Auto-restart-on-crash (containerization) is a separate parked item; cursor persistence makes auto-restart strictly more useful when it lands but doesn't depend on it. The architectural pattern: each component holds enough state to recover its own role from atproto + a tiny local cursor; the swarm-wide invariant ("every committed signal eventually gets routed and replied to") emerges from those local recoveries rather than from a global coordinator.

### Lessons Learned

1. **PDS env changes require `docker compose up -d --force-recreate pds`**, not just `restart`. Restart doesn't reload env files.
2. **The PLC Dockerfile's default entrypoint is production-only.** Override the command to use `dist/bin.js`.
3. **`createRecord` with a fixed `rkey` (like `"self"` for profiles) is not idempotent.** Use `putRecord` for upserts.
4. **Lexicon validation is opt-in.** `validate: false` required for custom lexicons.
5. **No published `:latest` tag for the PLC directory image.** Build from source.
6. **Handle domain must be `.test` in PDS dev mode.** `.localhost` rejected as invalid TLD.
7. **The relay validates hostnames strictly.** Bare service names (`pds`) and `host:port` formats are rejected. Needs a proper domain name. `trusted-domains` only affects account limits, not SSRF bypass.
8. **DID documents bake in the PDS URL at account creation.** Changing `PDS_HOSTNAME` requires wiping volumes and recreating accounts.
9. **The A2A ecosystem is in flux.** The spec recently moved to PascalCase method names (1.0). Python SDK 0.3.x and Go SDK 2.x disagree on method names. Agents need to be on matching SDK versions.

---

## Phase 3: Dynamic Agent Discovery — Complete

The bridge populates its registry from the PDS on startup (`listRepos` + `listRecords`), then keeps it live by watching the firehose for `community.swarm.agent.profile` create/update/delete events. Profile registration is no longer gated on a liveness check — the gate created an unrecoverable state when an agent was briefly down at bridge-restart time. Dispatch already handles unreachable endpoints visibly. The registry shrank to a tiny `DID → endpoint` cache after the Phase 4 capability-tag removal. Profile deletes remove entries; the `ManagedByBridge` flag on registry entries is forward-compatibility insurance for the gateway-as-onramp future.

---

## Phase 4: Human onramp, mention dispatch, and triage — Core Complete

**Goal:** Humans post natural-language signals via a CLI, the bridge dispatches by @-mention with conversation continuity, and a triage agent routes signals that arrive without explicit mentions.

The core path is built and verified end to end. Carlos posts a tagless signal via the CLI; the triage agent, subscribed to the firehose directly, picks it up; triage discovers agents from the bridge's `/agents` endpoint, fetches their A2A cards, calls an LLM via gateway, and publishes a `type: routing` signal in the thread under its own identity; the bridge sees that routing signal and dispatches to the chosen agent(s); the agent replies; multi-turn chat continues with per-agent context.

### Architectural principles (load-bearing)

- **The bridge knows about structure, never about content.** Mentions, thread refs, A2A correlation IDs, DIDs — yes. What a signal *means* — no. Anything that needs to read content lives in an agent.
- **Two classes of agents:**
  - **A2A-native agents** — speak A2A only, protocol-naive. They do work. Adoption story: any team with an A2A endpoint can plug in. Most agents are this.
  - **atproto-native agents** — opt into direct firehose subscription and PDS write access. Reserved for infrastructure-adjacent agents that benefit from observing the bus directly (currently just triage). Not a requirement for contributors.
- **atproto is the source of truth for thread state.** Conversation history lives in the public thread. Agents stay stateless across turns; the bridge threads A2A `taskId`/`contextId` through reply records so agents can resume the same task on the next turn.
- **AgentGateway has two valid roles**: LLM router (live), agent registration onramp (future direction, deferred). Never in the A2A dispatch path.

### Bridge routing rules (in order)

1. Signal has explicit `mentions` → dispatch to those agents.
2. Signal type is `request`, no mentions, and the parent's responsible agent resolves to a known agent → dispatch to that agent (chat continuation), passing the parent's `contextId` for continuity. The "responsible agent" is `parent.a2a.agentDid` when present (the canonical case for replies to a result signal), falling back to the parent record's plain author DID when not (the case for replies to agent-initiated requests like a `threeaday` nudge — the parent has no `a2a` field but its author *is* a registered agent). **Only fires for `request`-type signals** — result signals are audit records and must not trigger dispatch (doing so creates feedback loops). Triage's firehose filter applies the same widened rule, so the bridge and triage don't both dispatch on user replies to agent-initiated requests.
3. None of the above → drop. (Tagless requests are not dropped *on purpose*: the firehose-native triage is also subscribed to the relay, picks up the request asynchronously, and publishes a `routing` signal. That routing signal arrives back at the bridge via Rule #1 and triggers dispatch.)

**Specialist-to-orchestrator result delivery**: When the bridge dispatches via rule #1 because of a `routing` signal that carries `a2a.agentDid` (i.e., an agent's `/publish` signal with a back-reference), the reply signal is published with an explicit `mentions` entry for that agent. This means the result re-enters via rule #1, not rule #2. Without this, restricting rule #2 to `request` signals would drop specialist results before they reach the orchestrator.

**Triage routing signals vs. orchestrator routing signals**: Both are `type=routing` with non-empty `mentions`. The bridge tells them apart *structurally* by the absence/presence of an `a2a` field — triage routing signals have none; orchestrator `/publish` routing signals carry `a2a.agentDid` back-references. Two code paths depend on this distinction: (a) the "fetch parent content instead of rationale" override (applies only to triage-authored routing signals), and (b) the "reply-parent at thread root, not the routing signal" rule (same). Never key this off handle identity — the bridge is handle-agnostic.

### Remaining Phase 4 work (iterative)

The structure is in place; what's left is the kind of work that surfaces only by using the system.

1. **Bridge support for `input-required` task status.** Currently the bridge treats every A2A response as a completed task — it extracts the text and publishes a reply signal. But a third-party agent may return `input-required` (e.g., "which city?") instead of completing. The bridge needs to: recognize the non-terminal status, publish the response as a reply, and when the user follows up in the thread, send the follow-up to the *same taskId* (not just contextId) so the agent can resume the open task. This is the bridge becoming a proper A2A citizen — we can't dictate how third-party agents use task lifecycle. Key design question: does the bridge need to @ the original user to signal that input is needed, or is the reply in the thread sufficient?
2. **Iterate on triage prompts and behavior.** The prompt now instructs triage to always route somewhere (orchestrator as fallback for non-specialist signals). Things to test:
   - Multi-intent signals (e.g., "remind me to buy milk and check pkg 99") — does triage pick multiple agents?
   - Ambiguous signals that span domains (e.g., "will my package arrive before the rain stops?") — does triage pick both, or fall back to orchestrator?
   - Edge cases where the LLM returns malformed JSON — the parser is tolerant but we should know what surfaces.
3. ~~Add a second domain agent~~ — **done.** Weather agent (LLM-backed) gives triage real ambiguity to resolve.
4. ~~Chat memory verification~~ — **done.** Weather and orchestrator agents reconstruct conversation history from contextId-grouped tasks. Verified: ask about Austin weather, follow up without naming the city, agent remembers. Each turn is its own completed task; contextId is the grouping key, not a task-resumption mechanism.
5. ~~Orchestrator decomposition~~ — **done.** The orchestrator decomposes compound queries into sequential sub-dispatches via `POST /publish`. Each sub-dispatch is a routing signal with mentions and an `a2a` field pointing back to the orchestrator. Specialist replies arrive via implicit A2A continuation. The orchestrator drives its own loop; the bridge is stateless per signal. Each sub-query and specialist reply is published as a signed signal in the atproto thread. See decisions #12 and #13.
6. ~~PDS session refresh~~ — **done.** The bridge's publish layer detects 401 from the PDS, refreshes (or re-creates) the session for the affected handle, and retries. Shipped in commit `71fedce`.
7. ~~Restart durability for conversational context~~ — **done.** Implemented as the Thread-Context A2A Extension v1 (decision #15). Agents survive restart mid-thread by reconstructing prior turns from the atproto thread on the very next dispatch, via history forwarded by the bridge in the A2A message. Adopted by weather, threeaday, and orchestrator; triage is exempt by design. Shipped in commit `f5524fa`.
8. ~~Real-time human client~~ — **done.** AppView (decision #14) gives humans login, live thread view, compose, inline reply, and My Feed/Firehose tabs in the browser. Shipped in commits `a8641e2`, `d2b892d`, `f5524fa`.
9. ~~A `publish-profile` CLI subcommand~~ — **done.** `./cli/murmur publish-profile --handle <h> --name <n> --description <d> --endpoint <url>` authenticates as the agent and writes the `community.swarm.agent.profile` record at `rkey="self"`. Also accepts `--file <path>` with a JSON manifest (single object or array of profiles); `scripts/profiles.json` is the checked-in sandbox manifest, so post-wipe re-publish is `./cli/murmur publish-profile --file scripts/profiles.json`. Falls back to the sandbox-convention password (`<handle-base>-sandbox-pass`) if `--password`/`MURMUR_PASSWORD`/manifest password are all unset. Replaces five near-identical 25-line fish-curl blocks in the runbook.
10. **Loop protection beyond own-publisher dedup.** As more swarm agents come into play, we may need provenance fields on signals or sender-class filters to prevent unintended cascades. Not urgent yet — partly addressed today by the agent-registry-aware filters in both the bridge (rule #2 widening) and triage.
11. **Multi-agent threads.** Post signals where triage picks multiple agents. Observe how the conversation reads.
12. ~~AppView startup backfill from the PDS~~ — **done.** At startup the AppView walks `com.atproto.repo.listRecords` for `community.swarm.signal` across every known repo (the configured human handle resolved via `com.atproto.identity.resolveHandle`, plus every agent in the bridge's discovery registry), reconstructs `*Signal` objects via the same `recordToSignal` path the firehose uses, and feeds them through a new `AddSignalSilent` variant that indexes without WebSocket broadcasts. `AddSignal` deduplicates by URI, so backfill and firehose can safely overlap. Verified end-to-end against a non-trivial sandbox (commit `82c3d21`): cold-start backfill pulled 372 records across 6 authors in ~48 ms, deduped to 88 threads with all three signal types (88 requests, 58 routings, 60 results) and all 6 known authors participating. The bridge's per-dispatch `/api/thread` query now returns full history for any thread already on the PDS, regardless of when the AppView was last restarted.

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
- **AgentGateway as the registration onramp** — see section below.
- **Private communication patterns** — DMs as human-to-agent private channel, encrypted records, or separate access-controlled channels for sensitive coordination
- **Retention/archival** — when repo growth from signal threads becomes a concern
- **Async A2A support** — push notification callbacks in the bridge for long-running agent tasks (the delivery agent's async flow exposed this gap)

---

## Future direction: AgentGateway as the registration onramp

Recorded here so future-us doesn't re-derive it. This is a design we believe in but aren't building yet — it should land when there's a concrete reason (a second agent we don't want to hand-onboard, a team to demo onboarding to, real adoption pressure).

### The problem this solves

Today the bridge expects *someone* to publish a `community.swarm.agent.profile` record to the PDS for each agent. In the sandbox that someone is Carlos running curl. In an enterprise it would have to be the team that owns the agent, and that ask is too big: it requires them to learn atproto, hold a DID, sign records, and care about a custom lexicon — all to register an A2A backend they already have. The friction kills adoption. Teams will route around the swarm and you lose the network effect.

The earlier "bridge syncs profiles into gateway routes" idea didn't help because it kept atproto as the source of truth, so teams still had to publish profiles. It also created bidirectional sync between two registries, which is what we removed in commit `8ec5424`.

### The shape

Invert the source of truth. **AgentGateway becomes the registration interface; atproto becomes the projection.**

A central platform team (or a small portal in front of gateway) accepts agent registrations. The ask to an agent team is the smallest possible: *"give us your agent card URL and the team that owns it."* That's it. Most teams already have a card. They learn nothing new.

Gateway holds the canonical list of registered agents — a route per agent pointing at the agent's backend, plus the agent card URL as metadata on the route. The bridge watches gateway (polling `GET /config` is fine — we proved this works in commit `26d86e1`) and reconciles atproto state to match: for every gateway entry, the bridge ensures a corresponding atproto account, profile record, and current capability tags exist. For entries removed from gateway, the bridge tombstones the profile.

Because the flow is **one-way** (gateway → atproto) there is no drift problem. atproto can't disagree with gateway because atproto isn't a source. This is the version of "use gateway" that doesn't have the architectural smell.

### What the bridge gains as responsibilities

1. **Watch gateway** for the set of registered agents and their card URLs.
2. **Mint atproto identities lazily.** When a new entry shows up in gateway, the bridge calls `com.atproto.server.createAccount` and gets back a DID + handle. The bridge records the mapping `gateway_entry_id → did`.
3. **Custody signing keys.** The bridge holds the signing key for every agent it minted. Sandbox: a sqlite file. Production: KMS/HSM. This is a real new operational concern but a tractable one — it's the same shape as any service that signs on behalf of users.
4. **Publish profiles on agents' behalf.** The bridge writes `community.swarm.agent.profile` records into each minted account, with capabilities pulled from the agent card.
5. **Sign reply signals as the actual agent**, not as `bridge.test`. This is an upgrade on the current state: today reply signals are attributed to the bridge ("the bridge says agent X produced this"). With per-agent DIDs minted by the bridge, replies can be signed by the agent's own DID, even though the agent has no idea atproto exists. The audit story becomes structural, not just logged.
6. **Lifecycle management.** Profile updates when the agent card changes; tombstones when entries are removed from gateway.

### What atproto is for, in this model

Worth being explicit about, because it changes from the original framing. atproto is no longer the registration protocol. It is still:

- The **coordination substrate** — signals, threads, replies, routing
- The **audit trail** — every dispatch and reply, signed and durable
- The **identity layer** — every agent has a DID, even if the bridge minted it
- The **federation surface** — how multiple PDSes / orgs connect

What it is *not* anymore is the place agents announce themselves. That moves up a level into a registration step that humans interact with.

### Coexistence with self-published profiles

Agents that *do* want to participate natively (publish their own profiles, hold their own DIDs) are still fully supported. The bridge's reconciler only manages the profiles it minted — tracked by the `gateway_entry_id → did` mapping. Self-published profiles arrive via the firehose path that already exists today and stay out of the reconciler's scope. Two origins, no conflict, both reach the same registry.

This is why the small forward-compatibility step in Phase 3 matters: when we add profile updates and deletes, the registry entry should carry a flag distinguishing "profile I manage" from "profile someone else published." Cheap insurance for the day there are two origins.

### Concerns

- **The bridge gets bigger.** It now does dispatch *and* identity/lifecycle. All of these fit its existing role as the seam between protocols, but there's a real argument for splitting it later into a stateless **dispatcher** (firehose → A2A → reply) and a stateful **reconciler** (gateway → atproto identity/profile lifecycle). They share nothing operationally. Keeping them logically separated from day one makes the eventual split cheap.
- **Key custody is a real responsibility.** Encrypted at rest, rotation story, recovery story. Not hard, but not free either. Worth designing deliberately rather than accreting.
- **Gateway doesn't natively model "agent cards" as first-class.** It has routes and backends. The card URL would attach as route metadata, or live in a small sidecar config the bridge reads alongside gateway config. Small design choice, both work.
- **Polling gateway is the simple watch mechanism.** Webhooks would be nicer but don't exist. Polling `GET /config` on a timer is fine for the scale this would operate at.

### What this implies for nearer-term work

- **Phase 3 update:** when adding profile update/delete handling, include a `managed_by_bridge` (or equivalent) field on registry entries. Pure forward-compatibility — no cost today.
- **Phase 4 reconsideration:** a CLI that publishes agent profiles directly to atproto becomes *less* central in this model, because direct publishing isn't the primary onramp. It's still useful for testing and for the self-published path, but it's no longer the main story for "how does an agent get into the swarm."

---

## Swarm agents: what we built, what we deferred

Recorded so future-us doesn't re-derive the reasoning. The Phase 4 section above is the "what to build" view; this section is the "why this shape" view, plus notes on the runtime concept we deferred.

### Recap of the principle

The bridge is structurally dumb: it knows about mentions, thread refs, and A2A correlation IDs — never about content. Anything that requires reading what a signal *says* — triage, classification, summarization, dedup, correlation, severity tagging — lives outside the bridge.

### What we built vs. what we deferred

**Built:** Triage as an **atproto-native** Python service in `~/projects/agents/triage/`. It:
- Subscribes to the relay firehose directly using the `atproto` SDK.
- Filters for tagless request signals (skipping self-authored, non-request types, and implicit-continuation signals whose parent has `a2a.agentDid` — Rule #2 territory).
- Discovers available agents via the bridge's HTTP `/agents` endpoint (unchanged from before).
- Fetches each agent's A2A card for rich skill descriptions.
- Calls an LLM (via AgentGateway) to pick the right agent(s).
- Publishes a `routing` signal directly to its own PDS repo. The bridge sees the routing signal on the firehose and dispatches A2A to the mentioned agents.

Triage also still exposes an A2A endpoint for external callers who want synchronous routing; murmur itself no longer uses it. Triage is the one agent that opts into atproto-native operation — the cost is a direct relay subscription and PDS write access; the benefit is that the bridge has no triage-specific special case (no `TRIAGE_HANDLE`, no synchronous call, no fallback). User-facing A2A agents remain atproto-naive; the bridge mediates all atproto interaction on their behalf.

**Why atproto-native for triage specifically:** triage is infrastructure-adjacent, built by the same people who operate the swarm. Asking it to speak atproto doesn't raise the barrier for *contributed* agents — only for the reference triage implementation. In exchange the bridge becomes uniform ("dispatch on any mention-bearing signal"), which also opens the door to running multiple triages in parallel (different models, different prompts) as a live-eval harness where the routing signal record itself becomes the log.

**Deferred:** A "swarm-agent runtime" with skills and an event bus. Triage now observes the firehose, but it does so with ~100 lines of subscription code, not a framework. A runtime would only earn its keep when we have agents that need cross-thread observation: summarization (when does a thread cross some length threshold?), dedup (have I seen something like this before?), correlation (do these two threads describe the same incident?), severity tagging based on long-window patterns. None exist yet. When they do, the runtime concept can come back — **but it would only host the cross-thread agents.** Per-signal classifiers like triage get by on a subscription coroutine.

### If/when we build the runtime (notes worth keeping)

These are design notes for the day a cross-thread observer becomes worth building. They are not commitments.

- **Skills should follow the [agentskills.io](https://agentskills.io) specification.** A skill is a directory containing at minimum a `SKILL.md` (YAML frontmatter + Markdown body) plus optional `scripts/`, `references/`, `assets/`. Required frontmatter: `name` (1-64 chars, lowercase letters, numbers, and hyphens, must match the parent directory name) and `description` (1-1024 chars). Optional: `license`, `compatibility`, `metadata` (a map of string keys to string values, intended for client-specific extensions), `allowed-tools` (experimental).
- **Activation is the place we'd extend the spec.** The spec defines packaging and progressive disclosure but doesn't prescribe activation. Existing hosts use description-based matching driven by user prompts. For autonomous event processing we'd add cheap metadata-based pre-filters (`swarm.activation.*` keys) so the runtime can skip skills without an LLM call. The extension lives in the spec-defined `metadata` field, so skills stay spec-conformant. Other agentskills-compatible hosts would parse the metadata as opaque strings and ignore the keys they don't recognize.
- **Trust isolation.** Mixing critical infrastructure skills with experimental ones in one process means a buggy optional skill can affect the critical path. Run multiple instances partitioned by trust level — same code, different skill set. Defer until it matters.
- **Loop protection** would need more than own-DID dedup. Sender-class filters (skills only act on human-posted signals), provenance fields on signals (so a skill can tell its own output apart from genuine new signals), or hop counters as a last-resort circuit breaker.
- **The runtime is bigger than it looks.** Firehose subscription, PDS publishing, identity management, skill loading, LLM invocation, dedup state. Worth being honest that this is a non-trivial service to operate, not "the bridge plus a few helpers."

### Routing as published signals (the thread is the trace)

Independent of the runtime question: triage publishes a `type: routing` signal in the thread whenever it decides how to route a signal, carrying the rationale alongside the chosen mentions. The routing signal IS the dispatch trigger — the bridge sees it on the firehose and dispatches the mentioned agents in a separate `handleSignal` call. The thread becomes a self-documenting trace of "human asked → triage decided who → domain agent answered," all signed and durable.

The same pattern extends to agent-initiated signals via `POST /publish`. The orchestrator publishes routing signals with mentions; those flow through the firehose and trigger dispatch. Every signal in the system — human posts, triage routing, orchestrator sub-dispatches, specialist replies — follows the same path. The firehose is the event bus.

This pattern would carry over unchanged to runtime-hosted skills if we ever build them.
