# Murmur Implementation Plan

This is the working implementation plan. It reflects what we've actually built and learned, not just what we theorized in the design doc.

## Current State

**Phases 1–4 (core path) are complete.** A human posts a natural-language signal, the bridge calls a triage agent over A2A, triage uses an LLM (via AgentGateway → watsonx) to decide which agents should handle it — choosing between multiple domain agents or falling back to the orchestrator for anything that doesn't fit a specialist. The bridge dispatches to those agents, the agents reply (via LLM for weather and orchestrator, hardcoded for delivery), and the conversation can continue across multiple turns with per-agent context preserved — all without any domain agent knowing atproto exists.

### What's Running

- **PLC directory** — built from `bluesky-social/did-method-plc` source, using the dev entry point (`dist/bin.js`, not the production `service/index.js` which expects `DB_CREDS_JSON`)
- **PostgreSQL** — backs the PLC directory
- **PDS** — official `ghcr.io/bluesky-social/pds:0.4` image, dev mode, behind Caddy for TLS
- **Caddy** — TLS termination with mkcert certs for `pds.murmur.internal`
- **Relay** — built from `bluesky-social/indigo` source with SSRF check disabled for local sandbox. Uses SQLite, watches our PDS via the admin `requestCrawl` API.
- **Bridge** — Go service consuming the relay firehose, dispatching A2A tasks by @-mention, publishing results back as threaded reply signals. Event-driven: each `handleSignal` call does one thing per signal (triage publishes a routing signal and stops; routing signals trigger dispatch when they return on the firehose). Exposes `POST /publish` so agents can initiate signals, plus `GET /agents` and `GET /healthz` on port 2471.
- **AgentGateway** — back as an LLM router (port 4000). `/watsonx` → gpt-oss-120b, `/bedrock` → mistral-large. Used by the triage agent (and any future swarm-side service) so model choice is a config decision, not code. Never on the A2A dispatch path.
- **Triage agent** — Python A2A service in `agents/triage/`. Called by the bridge over A2A when a signal has no mentions and no continuation context. Discovers available agents via the bridge's `/agents` endpoint, fetches each agent's A2A card directly, and asks an LLM via gateway which agents should handle the signal. Prompt instructs triage to always route somewhere — if no specialist fits, route to the orchestrator. For correlated multi-agent queries (where sub-queries reference each other), triage routes to the orchestrator instead of listing multiple specialists.
- **Weather agent** — Python A2A service in `agents/weather/`. LLM-backed domain agent that answers weather-related questions via gateway. Second domain agent, added so triage has real ambiguity to resolve.
- **Orchestrator agent** — Python A2A service in `agents/orchestrator/`. LLM-backed orchestrator that both handles general queries directly and decomposes compound queries across specialist agents. When a query needs multiple specialists (especially correlated ones like "where's my package and what's the weather *there*?"), the orchestrator publishes signals via the bridge's `POST /publish` endpoint — each sub-dispatch is a routing signal with mentions and an `a2a` field pointing back to itself. The bridge dispatches to each specialist via the normal firehose path, and forwards results back to the orchestrator. Each execute call is stateless; the orchestrator reconstructs conversation history from its task store. The whole cycle is visible in the atproto thread.
- **Murmur CLI** — Go binary in `cli/`. `post`, `reply`, `thread`, `agents`, `whoami`. Replaces the curl ceremony for posting and reading signals.
- Ports: PLC on 2582, PDS on 2583 (HTTPS on 443 via Caddy), Relay on 2470, Bridge discovery on 2471, AgentGateway LLM on 4000, delivery agent on 5006, triage on 5050, weather on 5051, orchestrator on 5052

### What We've Proven

- **The core thesis works.** A protocol-naive A2A agent (Python delivery-tracking agent) participates in the swarm without any knowledge of atproto. It receives tasks via standard A2A `SendMessage` and returns results. The bridge handles all atproto interaction.
- **@-mention dispatch works.** Signals carry a structured `mentions` field; the bridge resolves handles or DIDs to registered agents and dispatches by mention. Capability tags are gone — the agent card is the source of truth for what an agent does.
- **Multi-turn conversation memory works.** Each turn in a thread is its own A2A task, but tasks sharing a `contextId` are grouped. Agents reconstruct conversation history by querying their task store for all prior tasks with the same contextId and feeding the collected messages to the LLM. Verified end-to-end: ask about weather in Austin, follow up without naming the city, agent remembers. Key model insight: A2A tasks are units of work, not conversations. The thread is the conversation; `contextId` is the grouping key. The bridge sends only `contextId` on follow-ups (not `taskId`), so each turn gets a fresh task under the same context.
- **Implicit chat continuation works.** Replies in an agent thread don't need an explicit @ — the bridge sees `parent.a2a.agentDid`, treats that agent as the implicit mention, and dispatches with continuity. Analogous to Slack/Discord thread semantics.
- **LLM-based triage works and disambiguates.** Tagless top-level signals get routed to the right agent via an LLM call to the triage agent. With multiple domain agents registered (delivery + weather), triage correctly picks the relevant specialist. Signals that don't fit any specialist route to the orchestrator. The triage decision is published as a `type: routing` signal in the thread for audit visibility.
- **The orchestrator fallback works.** Signals that don't match a specialist agent reach the orchestrator and get a useful response. The bridge also has a safety net: if triage returns empty mentions despite its prompt, the bridge routes to the orchestrator rather than dropping the signal.
- **Orchestrator decomposition works via `/publish`.** Compound queries with correlated sub-queries (e.g., "when does my package arrive and what's the weather there?") are routed to the orchestrator by triage. The orchestrator publishes sub-dispatch signals via `POST /publish`, each with an `a2a` field pointing back to itself. The bridge processes each signal through the normal firehose path, dispatches to the specialist, and forwards the result directly to the orchestrator. The orchestrator uses earlier results to formulate later queries (learns "Austin, TX" from delivery, asks weather about Austin). Each execute call is stateless — history is reconstructed from the task store. No bridge-side orchestrator loop, no bespoke dispatch API.
- **Threading is correct.** Bridge replies set `reply.root` to the actual thread root and `reply.parent` to the directly-replied-to message, so multi-turn conversations form a coherent tree.
- **Discovery without atproto knowledge works.** The bridge exposes its agent registry via plain HTTP, so the triage agent (and any future service) can find what's in the swarm without learning atproto.
- **`POST /publish` enables agent-initiated signals.** Any agent can publish a signal as its own identity by calling the bridge's `/publish` endpoint. The signal is committed to the PDS and flows through the firehose for normal processing. This is the missing "write" primitive that makes agents first-class participants — they can initiate, not just react. The orchestrator uses it for sub-dispatch; future agents (calendar reminders, monitoring alerts) will use it to speak first.
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

7. **Bridge exposes an HTTP surface** (port 2471, configurable via `DISCOVERY_ADDR`). `GET /agents` returns the current registry. `POST /publish` lets agents write signals to atproto as their own identity — the bridge uses the caller's credentials (which it already holds) to sign the record. `GET /healthz` for liveness. This is the boundary between "things that touch atproto" (the bridge) and "things that don't" (triage, orchestrator, and any future swarm-side service).

8. **Triage is a regular A2A agent, not a swarm-runtime skill.** We initially sketched a Python "swarm-agent runtime" with skills, an event bus, and firehose subscription. We then realized that triage is a per-signal classifier: it takes a signal in, returns a routing decision out. It doesn't need cross-thread observation or skill orchestration. Making it a normal A2A agent (one Python service per agent, conforming to a2a-sdk) means it follows the same shape as any domain agent — easy to write, easy to swap, no new runtime to maintain. The bridge calls it via standard A2A `SendMessage`. The "swarm-agent runtime" concept is deferred entirely until we have a use case (cross-thread summarization, dedup, correlation) that genuinely needs firehose subscription and skill orchestration.

9. **Routing decisions are published as `type: routing` signals and ARE the dispatch triggers.** When the bridge calls triage and gets back mentions, it publishes a `routing` signal in the thread carrying the rationale — and stops. The routing signal returns on the firehose and triggers dispatch in a separate `handleSignal` call. Routing signals from agents (e.g., orchestrator via `/publish`) work the same way: the signal goes to atproto, comes back on the firehose, and the bridge dispatches the mentioned agents. One signal, one action. No double-dispatch because routing signals aren't published and dispatched in the same call — they're published, then dispatched when they return.

10. **The bridge signs records on behalf of agents whose credentials it holds.** Agent accounts on the PDS are part of the architecture — the bridge already has the right to hold their app passwords and publish under their identity. This makes the public thread a real signed audit trail: a routing signal authored by `triage-one.test` is *triage's key* signing the decision, not the bridge claiming on triage's behalf, and a result signal from `agent-one.test` is the delivery agent's key signing its own response. As of this slice, the bridge holds a per-handle session map populated from `MURMUR_AGENT_CREDS` (a comma-separated list of `handle:password` pairs read out of `.env`). Both routing signals and reply signals route through `sessionFor(handle)`, which falls back to bridge authorship when no credentials are registered for the requested handle. The firehose-side own-publisher check (`isOwnPublisher`) covers any DID the bridge publishes under, so the dedup works regardless of authorship. **Deferred:** bridge-side account provisioning. The current env-var list is fine through ~10 agents but doesn't scale, and it pushes credential management onto operators. The next iteration is for the bridge to own account creation: one PDS admin secret enters the bridge, and it derives everything else (creates accounts, generates app passwords, persists them in its own state, publishes profile records). That collapses agent onboarding into a single bridge admin call and removes the runbook's manual curl dance entirely. It's the same shape as the gateway-as-onramp future direction (decision #4), but hosted in the bridge instead of in AgentGateway — same architectural move, smaller blast radius, doesn't bring gateway back into the dispatch path.

12. **Orchestrator decomposition uses the same routing pattern as triage — no bespoke dispatch API.** We initially built a `POST /dispatch` endpoint on the bridge so the orchestrator could sub-dispatch to specialist agents. This worked but was a side-channel. We then had the bridge drive the orchestrator loop (parsing dispatch instructions, sub-dispatching, feeding results back). This worked too but made the bridge stateful — both protocol adapter and orchestrator driver. The final architecture: the orchestrator publishes signals via `POST /publish` and drives its own loop. The bridge is event-driven and stateless per signal. See decision #13.

13. **Event-driven bridge + `/publish` primitive.** The bridge was doing too much in a single `handleSignal` call: triage, routing signal, dispatch, reply signal — all synchronously. `isOwnPublisher` existed to prevent double-dispatch from this design. We split it: each `handleSignal` does one thing. Triage publishes a routing signal and stops. The routing signal returns on the firehose and triggers dispatch. Reply signals are published for the audit trail. `isOwnPublisher` was replaced with a targeted skip: only result signals from held DIDs are skipped (they're audit records for completed dispatches). Routing signals from held DIDs flow through normally — they're dispatch triggers. `POST /publish` is the write primitive that lets any agent initiate a signal. The orchestrator uses it with an `a2a` field pointing back to itself; the bridge forwards specialist results to the orchestrator via direct A2A delivery (result signals are skipped on the firehose, so this direct forwarding is the delivery mechanism). Key insight: the bridge never needed a loop. Published signals are events. The firehose is the event bus.

11. **A2A tasks are units of work, not conversations.** Each turn in a thread creates and completes its own A2A task. Tasks sharing a `contextId` are grouped; agents reconstruct conversation history by querying their task store for all prior tasks with the same contextId. The bridge sends only `contextId` on follow-ups (not `taskId`), so each turn gets a fresh task under the same context. This means agents are stateless across turns — the task store is a performance optimization (gives the LLM prior context), not a correctness requirement. If an agent restarts and loses its in-memory store, the next turn starts fresh; the atproto thread still has the full record. **Not yet handled:** the `input-required` task status, where a third-party agent keeps a task open across turns. That requires the bridge to send `taskId` (not just `contextId`) when resuming an open task — a separate slice.

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

The core path is built and verified end to end. Carlos posts a tagless signal via the CLI; the bridge calls triage; triage discovers agents from the bridge's `/agents` endpoint, fetches their A2A cards, calls an LLM via gateway, and returns a routing decision; the bridge publishes a `type: routing` signal in the thread for audit and dispatches to the chosen agent(s); the agent replies; multi-turn chat continues with per-agent context.

### Architectural principles (load-bearing)

- **The bridge knows about structure, never about content.** Mentions, thread refs, A2A correlation IDs, DIDs — yes. What a signal *means* — no. Anything that needs to read content lives in an agent.
- **Two classes of agents:**
  - **Domain agents** — A2A-only, protocol-naive. They do work. Adoption story: any team with an A2A endpoint can plug in.
  - **Swarm agents** — built by whoever operates the swarm; called by the bridge for content reasoning. Triage is the first one, and it's just a normal A2A agent. (See decision #8 above for why we *didn't* build a runtime.)
- **atproto is the source of truth for thread state.** Conversation history lives in the public thread. Agents stay stateless across turns; the bridge threads A2A `taskId`/`contextId` through reply records so agents can resume the same task on the next turn.
- **AgentGateway has two valid roles**: LLM router (live), agent registration onramp (future direction, deferred). Never in the A2A dispatch path.

### Bridge routing rules (in order)

1. Signal has explicit `mentions` → dispatch to those agents.
2. Signal has no mentions but `parent.a2a.agentDid` resolves to a known agent → dispatch to that agent (chat continuation), passing the parent's `contextId` for continuity.
3. No mentions, no continuation context → call the triage agent over A2A (handle from `TRIAGE_HANDLE`, default `triage.test`). Use the returned `mentions`. Publish a `routing` signal in the thread for audit.
4. None of the above → drop.

### Remaining Phase 4 work (iterative)

The structure is in place; what's left is the kind of work that surfaces only by using the system.

1. **Bridge support for `input-required` task status.** Currently the bridge treats every A2A response as a completed task — it extracts the text and publishes a reply signal. But a third-party agent may return `input-required` (e.g., "which city?") instead of completing. The bridge needs to: recognize the non-terminal status, publish the response as a reply, and when the user follows up in the thread, send the follow-up to the *same taskId* (not just contextId) so the agent can resume the open task. This is the bridge becoming a proper A2A citizen — we can't dictate how third-party agents use task lifecycle. Key design question: does the bridge need to @ the original user to signal that input is needed, or is the reply in the thread sufficient?
2. **Iterate on triage prompts and behavior.** The prompt now instructs triage to always route somewhere (orchestrator as fallback for non-specialist signals). Things to test:
   - Multi-intent signals (e.g., "remind me to buy milk and check pkg 99") — does triage pick multiple agents?
   - Ambiguous signals that span domains (e.g., "will my package arrive before the rain stops?") — does triage pick both, or fall back to orchestrator?
   - Edge cases where the LLM returns malformed JSON — the parser is tolerant but we should know what surfaces.
3. ~~Add a second domain agent~~ — **done.** Weather agent (LLM-backed) gives triage real ambiguity to resolve.
4. ~~Chat memory verification~~ — **done.** Weather and orchestrator agents reconstruct conversation history from contextId-grouped tasks. Verified: ask about Austin weather, follow up without naming the city, agent remembers. Each turn is its own completed task; contextId is the grouping key, not a task-resumption mechanism.
5. ~~Orchestrator decomposition~~ — **done.** The orchestrator decomposes compound queries into sequential sub-dispatches. No bespoke bridge API was needed — the orchestrator returns dispatch instructions in the same format as triage (`{"mentions": [...], "content": "..."}`), and the bridge drives the loop. Each sub-query and specialist reply is published as a signed signal in the atproto thread. See decision #12.
6. **PDS session refresh.** Bridge JWTs expire after a few hours. Currently requires a bridge restart. The publish layer should detect 401 from the PDS, call `createSession` again for the affected handle, update the session, and retry.
7. **A `publish-profile` CLI subcommand.** Every new agent needs the curl-based profile publish dance. Worth automating.
8. **Loop protection beyond own-publisher dedup.** As more swarm agents come into play, we may need provenance fields on signals or sender-class filters to prevent unintended cascades. Not urgent yet.
9. **Multi-agent threads.** Post signals where triage picks multiple agents. Observe how the conversation reads.

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

**Built:** Triage as a regular A2A agent (`agents/triage/`). It's a Python service that:
- Receives an A2A `SendMessage` with the signal text
- Discovers available agents via the bridge's HTTP `/agents` endpoint (no atproto knowledge)
- Fetches each agent's A2A card directly to get rich skill descriptions
- Calls an LLM (via AgentGateway) to pick the right agent(s)
- Returns a JSON routing decision

The bridge calls triage over standard A2A. Triage is structurally identical to a domain agent — same SDK, same shape, same deployment pattern. It is *not* atproto-aware; the bridge mediates all atproto interaction on its behalf via the `/agents` discovery endpoint.

**Deferred:** A "swarm-agent runtime" with skills, an event bus, and firehose subscription. We initially sketched this as the home for triage and any future swarm-side function. We then realized triage doesn't need any of it: it's a per-signal classifier that takes content in and returns a decision out, with no cross-thread state and no need to observe the firehose directly. The same applies to anything else that's a per-signal function. Building a runtime to host one stateless classifier would be over-engineering.

A runtime would only earn its keep when we have agents that genuinely need cross-thread observation: summarization (when does a thread cross some length threshold?), dedup (have I seen something like this before?), correlation (do these two threads describe the same incident?), severity tagging based on long-window patterns. None of those exist yet, and we don't have a use case demanding them. When we do, the runtime concept can come back — **but it would only host the cross-thread agents.** Per-signal classifiers like triage will stay as plain A2A services regardless.

### If/when we build the runtime (notes worth keeping)

These are design notes for the day a cross-thread observer becomes worth building. They are not commitments.

- **Skills should follow the [agentskills.io](https://agentskills.io) specification.** A skill is a directory containing at minimum a `SKILL.md` (YAML frontmatter + Markdown body) plus optional `scripts/`, `references/`, `assets/`. Required frontmatter: `name` (1-64 chars, lowercase letters, numbers, and hyphens, must match the parent directory name) and `description` (1-1024 chars). Optional: `license`, `compatibility`, `metadata` (a map of string keys to string values, intended for client-specific extensions), `allowed-tools` (experimental).
- **Activation is the place we'd extend the spec.** The spec defines packaging and progressive disclosure but doesn't prescribe activation. Existing hosts use description-based matching driven by user prompts. For autonomous event processing we'd add cheap metadata-based pre-filters (`swarm.activation.*` keys) so the runtime can skip skills without an LLM call. The extension lives in the spec-defined `metadata` field, so skills stay spec-conformant. Other agentskills-compatible hosts would parse the metadata as opaque strings and ignore the keys they don't recognize.
- **Trust isolation.** Mixing critical infrastructure skills with experimental ones in one process means a buggy optional skill can affect the critical path. Run multiple instances partitioned by trust level — same code, different skill set. Defer until it matters.
- **Loop protection** would need more than own-DID dedup. Sender-class filters (skills only act on human-posted signals), provenance fields on signals (so a skill can tell its own output apart from genuine new signals), or hop counters as a last-resort circuit breaker.
- **The runtime is bigger than it looks.** Firehose subscription, PDS publishing, identity management, skill loading, LLM invocation, dedup state. Worth being honest that this is a non-trivial service to operate, not "the bridge plus a few helpers."

### Routing as published signals (the thread is the trace)

Independent of the runtime question: the bridge publishes a `type: routing` signal in the thread whenever it calls triage and gets back a decision, carrying the rationale alongside the chosen mentions. The routing signal IS the dispatch trigger — it returns on the firehose and the bridge dispatches the mentioned agents in a separate `handleSignal` call. The thread becomes a self-documenting trace of "human asked → triage decided who → domain agent answered," all signed and durable.

The same pattern extends to agent-initiated signals via `POST /publish`. The orchestrator publishes routing signals with mentions; those flow through the firehose and trigger dispatch. Every signal in the system — human posts, triage routing, orchestrator sub-dispatches, specialist replies — follows the same path. The firehose is the event bus.

This pattern would carry over unchanged to runtime-hosted skills if we ever build them.
