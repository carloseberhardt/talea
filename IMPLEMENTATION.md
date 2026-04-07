# Murmur Implementation Plan

This is the working implementation plan. It reflects what we've actually built and learned, not just what we theorized in the design doc.

## Current State

**Phase 1 and Phase 2 (core loop) are complete.** We have a working end-to-end atproto ↔ A2A bridge: Carlos posts a signal, the bridge routes it to an A2A agent, the agent responds, and the result is published back as a threaded reply — all without the agent knowing atproto exists.

### What's Running

- **PLC directory** — built from `bluesky-social/did-method-plc` source, using the dev entry point (`dist/bin.js`, not the production `service/index.js` which expects `DB_CREDS_JSON`)
- **PostgreSQL** — backs the PLC directory
- **PDS** — official `ghcr.io/bluesky-social/pds:0.4` image, dev mode, behind Caddy for TLS
- **Caddy** — TLS termination with mkcert certs for `pds.murmur.internal`
- **Relay** — built from `bluesky-social/indigo` source with SSRF check disabled for local sandbox. Uses SQLite, watches our PDS via the admin `requestCrawl` API.
- **Bridge** — Go service consuming the relay firehose, dispatching A2A tasks, publishing results back as threaded reply signals
- Ports: PLC on 2582, PDS on 2583 (HTTPS on 443 via Caddy), Relay on 2470

### What We've Proven

- **The core thesis works.** An A2A agent (Python delivery-tracking agent) participated in the swarm without any knowledge of atproto. It received a task via standard A2A `SendMessage` and returned a result. The bridge handled all atproto interaction.
- Custom lexicon records (`community.swarm.agent.profile`, `community.swarm.signal`) work with `validate: false`
- Threaded signals work: reply references via `strongRef` (URI + CID)
- The firehose delivers full record content (not just references) — the bridge can route on capability tags without extra fetches
- Agent profiles on the firehose can trigger dynamic registration in the bridge's agent registry
- The bridge's self-dedup (skip own DID) prevents firehose loops
- A2A protocol recently moved to 1.0 spec with PascalCase method names (`SendMessage` not `message/send`). Go SDK (`a2a-go/v2`) is current. Python SDK `a2a-sdk` 1.0.0a0 aligns; older 0.3.x uses the old names.

### Accounts

| Handle | DID | Role |
|---|---|---|
| carlos.test | (regenerated after hostname change) | Human |
| agent-one.test | (regenerated) | Agent |
| bridge.test | (new) | Bridge infrastructure |

### Infrastructure Decisions Made

1. **PDS hostname is `pds.murmur.internal`** — Docker network alias + `/etc/hosts` entry. Required because DID documents bake in the PDS URL at account creation, and the relay needs to reach the PDS from inside Docker.
2. **Caddy for TLS** — mkcert local CA. The relay's SSRF protection and OAuth/client libraries are happier with real HTTPS. `mkcert -install` trusts the CA system-wide.
3. **Relay built from source** — The official Indigo relay has hardcoded SSRF protection that blocks connections to private IPs. We patched `util/ssrf/ssrf.go` to disable `PublicOnlyControl` for the sandbox. This is a one-line change in the Go source.
4. **Bridge dispatches directly to agents** — not through AgentGateway. AgentGateway integration was built and proven in commit `26d86e1` (its admin API on port 15000 supports runtime config updates via full-config replacement, not fine-grained CRUD), then removed from the dispatch path. The reason: putting the gateway in the dispatch path created a second registry — agent profiles in atproto repos *and* routes in AgentGateway — and made the bridge responsible for keeping the two in sync. Reconciliation would have made the bridge a worse SPOF without buying us anything the sandbox actually needs yet. The bridge now reads agent profiles from the firehose, keeps an in-memory registry, and dispatches directly to each agent's `a2aEndpoint`. AgentGateway can come back when we have a concrete need (observability of A2A traffic, policy enforcement, LLM proxy) that the bridge alone can't satisfy. The dispatch destination is a single per-call decision, so swapping it for a gateway URL later is a small change.
5. **Full results in reply signals** — the bridge publishes complete A2A results back to atproto threads, not just metadata. Network-level trust model for the sandbox (see design doc security section).
6. **AgentGateway returns in Phase 4 — but as an LLM router, not an A2A router.** Different role from the dispatch-path use we removed in commit `8ec5424`. The Phase 4 swarm agent makes its LLM calls through gateway (port 4000) so model selection (gpt-oss-120b on watsonx, Bedrock, local, anthropic) becomes a config decision instead of a code change. Config lifted from `~/projects/agent-skill-poc/infra/agent-gateway/config.yaml`. Gateway is *never* in the A2A dispatch path; the bridge still dispatches directly to each agent's `a2aEndpoint`. The two roles of gateway in this architecture (LLM router, immediate; agent registration onramp, future) are completely separate and don't conflict.

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

## Phase 3: Dynamic Agent Discovery

**Goal:** Agents join the swarm dynamically — no bridge restarts, no config file changes — and the bridge can be restarted at any time without losing track of who's out there.

### What Needs to Happen

The bridge already watches the firehose for `community.swarm.agent.profile` records and registers them in its in-memory registry. It also already fetches the A2A agent card from each agent's `a2aEndpoint` to merge skill tags from the card with the tags declared in the atproto profile. What's missing:

1. **Startup state rebuild from the PDS, not the firehose.** On startup the registry is empty until new profiles flow through the firehose. The bridge should query the PDS for the current set of profiles directly: `com.atproto.sync.listRepos` to enumerate repos, then `com.atproto.repo.listRecords` with `collection=community.swarm.agent.profile` per repo. This treats the PDS as the source of truth (which it is) and the firehose as a notification stream for *changes*. Replaying the firehose from a cursor would work but is the indirect answer — replay is for event ordering, not current state.

2. **Profile updates and deletes.** Today only `create` actions populate the registry. Updates should refresh the entry; deletes (or a future `status: offline` field) should remove or mark it.

3. **Liveness at registration time.** The agent card fetch already doubles as a one-shot reachability check. Worth surfacing it explicitly in logs and skipping registration if the agent isn't reachable. Ongoing health monitoring is *not* the bridge's job — that's gateway/observability work for whenever gateway comes back.

### What We'll Discover

- Whether `listRepos` + `listRecords` is fast enough to be done synchronously at startup, or whether the bridge needs to come up serving an empty registry and backfill in the background.
- Whether the atproto profile + A2A agent card alignment works in practice (already partially answered — the merge logic is there, but we don't yet have multiple agents to stress it).
- How to handle agents going offline. Profile deletion is the cleanest signal but agents may not clean up after themselves.

---

## Phase 4: Human onramp, mention dispatch, and the first swarm agent

**Goal:** Humans post natural-language signals via a CLI, the bridge dispatches by @-mention with conversation continuity, and the first swarm agent (running a triage skill) handles signals that arrive without explicit mentions.

### Background: how this phase took shape

Phase 4 went through two reframings before settling:

1. **First version:** "implement a slow path in the bridge" — bridge runs an LLM triage function for tagless signals. Rejected because the bridge must not be content-aware, and because *every* human signal is tagless (humans don't tag), so there's no "fast path vs slow path" — there's just one path.

2. **Second version:** swarm agent triages tagless signals, classifies them into capability tags, publishes a re-tagged signal that the bridge dispatches via its existing capability-matching fast path.

3. **Final version (this one):** capability tags don't exist as a routing primitive at all. The bridge routes by @-mention only. The swarm agent's job is to decide which agents should handle a signal and publish a routing reply that @-mentions them.

The reasoning that collapsed (2) into (3): **the agent card already says what an agent does**. Maintaining a parallel `capabilities` field on atproto profiles duplicates information that lives authoritatively in the A2A agent card. The swarm agent uses the cards directly as routing inputs (LLM tool-use), and the routing decision becomes "@these agents" rather than "tag this with these capabilities." Cleaner data model, smaller bridge, single source of truth.

### Architectural principles (carrying forward)

- **The bridge knows about structure, never about content.** Capability tags, thread refs, mentions, DIDs — yes. What a signal *means* — no. Anything that requires reading what a signal says lives in a swarm agent.
- **Two classes of agents:**
  - **Domain agents** — A2A-only, protocol-naive. They do work. Adoption story: any team with an A2A endpoint can plug in.
  - **Swarm agents** — atproto-aware, firehose-subscribing. They decide what work happens. Built by whoever operates the swarm.
- **atproto is the source of truth for thread state.** Conversation history lives in the public thread. Agents stay stateless across turns; the bridge threads A2A correlation IDs (`taskId`, `contextId`) through reply records so agents can resume the same task on the next turn.
- **AgentGateway has two valid roles in this architecture:** LLM router for the swarm agent (Phase 4, immediate), and registration onramp for domain agents (future direction, deferred). It is *never* in the A2A dispatch path.

### What changes in the bridge

The bridge gets simpler in some places and slightly more involved in one. Net change is small.

**Removed:**
- Capability matching (`FindByCapability`, the `Tags` field on `RegisteredAgent`, the per-signal capability-overlap logic)
- The agent card fetch + tag merge in `handleAgentProfile` (the swarm agent does this for routing decisions; the bridge doesn't need it)
- Most of the registry's data model — it shrinks to essentially `DID → a2aEndpoint` (a tiny cache, populated from profile records on the firehose, kept for fast mention-dispatch lookups)

**Added:**
- **Mention parsing.** Read the structured `mentions` field on incoming signals (handles or DIDs). This is *not* content parsing — `mentions` is a typed field on the lexicon, populated by whoever publishes the signal.
- **Mention dispatch.** For each mention: resolve handle → DID (if needed) → endpoint (from cache), dispatch via A2A.
- **Conversation context threading.** When dispatching a reply within an existing thread, the bridge fetches the parent record, reads the `a2a.contextId` and `a2a.agentDid` off it, and includes the contextId in the A2A `SendMessage` so the agent's task store continues the same task. On the response, the bridge captures the (possibly same, possibly new) taskId/contextId and stores them on the reply signal it publishes. This makes multi-turn chat work without giving the bridge any content awareness.

The threading logic is purely structural: read parent record → pull correlation IDs off a typed field → pass through. The bridge never reads message content for routing or memory purposes.

### What the swarm agent looks like

A separate Python service in a new top-level `swarm-agent/` directory. Adapts the runtime structure from `~/projects/agent-skill-poc/runtime/` (skill loader + event bus + scheduler + FastAPI for monitoring) and adds a firehose subscription as a new event source. Skills register interest in firehose events with metadata-encoded activation conditions and run their behavior when those conditions match.

Key choices:

- **Python**, with [`uv`](https://github.com/astral-sh/uv) for environment management (matches the delivery agent and Solis POC).
- **LLM calls go through AgentGateway** (`http://localhost:4000/watsonx` to start, routing to gpt-oss-120b on watsonx.ai). The runtime uses an OpenAI-compatible client pointed at the gateway. Model identity is invisible to the runtime — gateway picks the backend.
- **Skills can use `langgraph.prebuilt.create_react_agent`** for their internal tool-use loop, the same way `~/projects/agent-skill-poc/mock-agents/data_agent.py` does. This is *inside* a skill, not at the runtime layer. The runtime stays a thin event-driven loop.
- **Skills follow the [agentskills.io](https://agentskills.io) specification.** A skill is a directory containing at minimum a `SKILL.md` (YAML frontmatter + Markdown body), and optionally `scripts/`, `references/`, `assets/`, or any other files the skill needs. Required frontmatter fields: `name` and `description`. Runtime-specific configuration (activation conditions, trigger type) lives in the spec's optional `metadata` field under a `swarm.*` namespace, so skills remain spec-conformant. We don't introduce a sidecar config file alongside `SKILL.md` — anything runtime-specific goes in `metadata`.
- **The swarm agent has its own DID** (`swarm-agent.test` or similar) and publishes signals like any other participant.

### Work

1. **Lexicon updates** (done as part of this phase's prep):
   - `community.swarm.signal`: add `mentions` field (array of handles/DIDs), add `a2a` field (object with `agentDid`, `taskId`, `contextId`) for result signals, add `routing` to the `type` knownValues, mark `capabilities` as deprecated.
   - `community.swarm.agent.profile`: drop the required `capabilities` field (mark deprecated), make `a2aEndpoint` required, update description to clarify the agent card is the source of truth for what the agent does.

2. **CLI for humans.** Go binary in `cmd/murmur/`. Optimized for fast capture, free-text first, terse output, thread retrieval. Writes signals with the structured `mentions` field when the user types `@handle`. Reads threads (root + all replies) for browse mode. Reuses the bridge's PDS auth code where it makes sense.

3. **Bridge refactor.** Three parts, smaller than they sound:
   - Rip out capability matching (delete `FindByCapability`, the `Tags` field, the `handleSignal` matching logic, the agent card fetch in `handleAgentProfile`, the tag merge).
   - Add mention dispatch: parse `mentions`, resolve, dispatch.
   - Add conversation context threading: walk parent record on replies, pull `a2a.contextId`, pass to `sendA2ATask`, capture and persist on the published reply.

4. **AgentGateway back into docker-compose.** Different role from before: LLM router on port 4000. Config lifted from `~/projects/agent-skill-poc/infra/agent-gateway/config.yaml` (the `binds[1]` block — port 4000, the LLM routing). Watsonx route uses `provider.openAI` so the swarm agent talks to it via the OpenAI Python SDK with a custom `base_url`. Update `docker-compose.yml`'s comment block to reflect the new role.

5. **Swarm-agent runtime** in a new `swarm-agent/` directory. Python, `uv`. Adapts the Solis runtime structure: skill loader, event bus, FastAPI app for status/monitoring. New piece: a firehose subscriber that emits events into the bus when signals match skill activation criteria. Skills loaded from `swarm-agent/skills/`. Authenticates to the PDS as its own account (`swarm-agent.test`) so it can publish.

6. **Triage skill** as the first skill. Activation: signal from a human DID, no `mentions`. Action: LLM call (via gateway) with the signal content + the list of available agents (built from profile records + each agent's A2A card description) presented as tools. The LLM picks which agents should handle the signal. The skill publishes a `type: routing` reply in the thread carrying the new `mentions`. The bridge sees the reply and dispatches.

7. **End-to-end test:** Carlos posts via CLI with no mention → swarm agent triages → publishes routing reply → bridge dispatches → domain agent answers → Carlos replies as a follow-up → bridge threads conversation context → domain agent continues the same task.

8. **A second swarm skill** to validate skill-pluggability. Probably `summarize-thread` (activated when a thread crosses N replies). Exercises a different activation pattern.

9. **Multi-agent threads.** Post signals where triage decides multiple agents are relevant. Observe how the conversation reads.

### What We'll Discover

- Whether mention-only routing feels expressive enough in practice, or whether some kind of "broadcast to all monitoring agents" pattern needs to come back as a non-tag mechanism
- How conversation continuity feels across longer threads — particularly whether the bridge's per-reply parent fetch is fast enough or wants caching
- Whether the swarm agent's tool-use approach to picking agents (LLM looks at agent card descriptions) actually picks the right agents, or whether we need richer hints
- Whether autonomous activation cost is tractable (every firehose event from a human → potentially an LLM call) and whether metadata pre-filters do enough work
- How loops are best prevented in practice: own-DID dedup is necessary but probably not sufficient — we may want sender-class filters or provenance fields on routing replies

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

## Future direction: Swarm agents as a skill runtime

Recorded so future-us doesn't re-derive it. The Phase 4 section above is the "what to build" view; this section is the "why this shape" view, plus the alignment with the agentskills.io standard.

### Recap of the principle

The bridge is structurally dumb: it knows about mentions, thread refs, and A2A correlation IDs — never about content. Anything that requires reading what a signal *says* — triage, classification, summarization, dedup, correlation, severity tagging — lives outside the bridge.

That work happens in **swarm agents**: agents that subscribe to the firehose, see signals as they appear, and publish new signals in response. They're atproto-aware by necessity (they can't be protocol-naive and still have firehose visibility), and they're built deliberately by whoever runs the swarm. They are *of* the swarm, not just *in* it.

There are two natural classes of agents in the architecture:

| | Domain agents | Swarm agents |
| --- | --- | --- |
| Protocol awareness | A2A only (naive about atproto) | atproto + A2A |
| Activation | Dispatched to by the bridge | Self-activate from firehose events |
| Source of truth | An external team / vendor / repo | The swarm operator |
| Examples | Delivery tracking, calendar, code search, support ticket creation | Triage, summarization, dedup, correlation, severity tagging, routing |
| Onboarding friction | Should be near zero (the gateway-as-onramp story) | Intentional infrastructure investment |

This is the load-bearing distinction. Most "should this be a bridge feature?" questions have clean answers if you ask "is this content-aware?" — if yes, it belongs in a swarm agent, not in the bridge.

### Why one runtime with skills (instead of N agents)

When you enumerate the things swarm agents need to do — triage signals, summarize threads, detect dupes, correlate across threads, tag severity, translate, digest — the shared infrastructure is enormous and the differentiated logic is small:

**Shared (the runtime):** firehose subscription, commit parsing, self-DID dedup, PDS authentication, signal publishing, thread structure, loop protection.

**Differentiated (each skill):** "given this event, should I do anything? if so, what should I publish?"

Building each as its own agent would mean duplicating the shared infrastructure N times, running N processes, managing N DIDs, hitting the relay with N firehose subscriptions. Almost all of the code would be boilerplate. The right shape is **one runtime that hosts many skills**: a thin host loop that handles infrastructure, a directory of skills that provide behavior. The runtime is small; the skills carry the intelligence.

This is the same shape modern agent harnesses have been converging on (Claude Code, Goose, Cursor, Codex, etc.) and the existence of the agentskills.io standard reflects that convergence.

### Alignment with the agentskills.io specification

We want our skills to conform to the [Agent Skills](https://agentskills.io) specification so that:

1. Skills are portable across runtimes (in principle a swarm skill could be loaded by any agentskills-compatible host, with the appropriate context plumbing)
2. We benefit from the broader ecosystem's tooling (validators, examples, conventions)
3. Skill authors don't need to learn a swarm-specific format

#### What the spec gives us

A skill is a directory containing at minimum a `SKILL.md` file (YAML frontmatter + Markdown body), and optionally `scripts/`, `references/`, `assets/`, or any other files the skill needs. Required frontmatter fields: `name` (1-64 chars, lowercase letters, numbers, and hyphens; no leading, trailing, or consecutive hyphens; must match the parent directory name) and `description` (1-1024 chars; should describe what the skill does and when to use it). Optional frontmatter fields: `license`, `compatibility` (1-500 chars), `metadata` (a map of string keys to string values, intended for client-specific extensions), and `allowed-tools` (experimental).

The spec describes a **progressive disclosure** loading model: the frontmatter (name and description, ~100 tokens per skill) is loaded at startup for all skills, the full `SKILL.md` body is loaded only when a host decides to activate the skill, and any resources under `scripts/`, `references/`, or `assets/` are loaded on demand. This is what lets a host scale to many skills without paying their context cost up front.

#### Where we extend the spec (carefully)

The spec defines a packaging and discovery format. It does *not* prescribe how a host decides when to activate a skill — that's left to the host. In practice, existing hosts (Claude Code, Goose, Cursor, and others on the agentskills.io adoption list) tend to drive activation by matching the user's current task against skill descriptions: the agent looks at descriptions, decides which are relevant, loads them, and proceeds. That works for interactive agents where every activation is in response to a user prompt.

Our use is **autonomous event processing**: every firehose event is a potential trigger, and asking the LLM "which skills are relevant?" on every commit message would be expensive. We need cheap pre-filtering before any LLM call.

The spec accommodates this through the `metadata` field, which it defines as a map of string keys to string values that "clients can use to store additional properties not defined by the Agent Skills spec." We define conventional keys under a `swarm.*` namespace:

```yaml
---
name: triage
description: Decide which agents should handle a signal that has no explicit @-mentions, and publish a routing reply in the same thread that @-mentions them. Use when a signal arrives without mentions and is from a human user.
metadata:
  swarm.activation.event: "signal_received"
  swarm.activation.sender-class: "human"
  swarm.activation.has-mentions: "false"
---
```

(Values are quoted strings because the spec restricts metadata values to strings.)

The runtime indexes these on startup, evaluates them cheaply against every firehose event, and only loads a skill's body and invokes an LLM for events that pass the prefilter. Skills without `swarm.activation.*` metadata fall back to whatever default activation strategy the runtime implements (likely description-based matching, the same approach existing interactive hosts use).

Because the extension lives entirely in the spec-defined `metadata` field, skills remain spec-conformant. Any other agentskills-compatible host would parse the metadata as opaque key-value strings and ignore the keys it doesn't recognize.

The spec's optional `scripts/`, `references/`, and `assets/` directories are available for skills that need bundled resources — we're not using them yet, but they're a natural place to grow into if atproto develops interactive primitives (button responses, rich render types, etc.) that warrant skill-bundled assets.

### Routing as published signals (the thread is the trace)

The triage skill (and any other routing-style skill) doesn't return its decision through some private channel. It **publishes a `type: routing` signal** in the same thread carrying the inferred `mentions`. The bridge sees that signal on the firehose, parses the mentions, and dispatches. The thread becomes a self-documenting trace of "human asked → triage decided who → domain agent answered," all signed and durable.

The bridge needs almost no special handling for routing signals. They're just signals with `mentions` set, and the bridge already knows how to dispatch on mentions. The `type: routing` value is informational — it lets thread readers (humans, AppViews) distinguish a routing decision from a substantive reply, but the bridge doesn't branch on it.

### Concerns to design around

1. **Cost per event.** Even with metadata pre-filters, an LLM-driven runtime that activates skills on every matching event has a non-trivial cost profile. Mitigations: aggressive metadata filtering, batching of related events, caching of recent classifications, and choosing model size per skill. None of these are blockers for a sandbox; all of them matter at scale.

2. **Loops.** A swarm skill publishes a signal. The signal hits the firehose. Another skill (or the same skill) sees it and reacts. Repeat. Mitigations:
   - **Self-DID dedup** (the runtime ignores its own posts) — necessary but not sufficient if multiple swarm-agent instances exist.
   - **Sender-class filter** (e.g., "triage only acts on human-posted signals") expressed via skill metadata.
   - **Provenance fields on routing signals** (e.g., `routedFrom: at://...`) so triage can tell its own output apart from genuine new signals.
   - **Depth/hop counters** on signals as a last-resort circuit breaker.

3. **Trust isolation.** Mixing critical infrastructure skills (triage) with playful or user-installable skills (translate-to-pirate, summarize-my-mentions) in one process means a buggy optional skill can affect the critical path. Mitigation: run multiple instances of the same runtime, partitioned by skill trust level. One instance loads only core infrastructure skills with a stable skill set; another loads experimental or user-installable skills. Same code, different config. Defer until it matters.

4. **The runtime is bigger than it looks.** Firehose subscription, PDS publishing, identity management, skill loading, LLM invocation, dedup state — it adds up. Worth being honest that this is a non-trivial service, not "the bridge plus a few helpers." It deserves its own repo path, its own tests, its own deployment story.

### Concrete sketch of the runtime

A first-pass shape, not a final design:

```
swarm-agent/
├── runtime/
│   ├── main.py            # entry point: load skills, wire bus, subscribe firehose, run
│   ├── skill_loader.py    # discover and parse SKILL.md files (frontmatter + body)
│   ├── event_bus.py       # pub/sub for firehose events and inter-skill triggers
│   ├── firehose.py        # atproto firehose subscription, emits events into the bus
│   ├── publisher.py       # PDS client for publishing signals as the swarm agent's DID
│   ├── llm.py             # OpenAI-compatible client pointed at AgentGateway
│   └── api.py             # FastAPI app for status/monitoring
├── skills/
│   ├── triage/
│   │   └── SKILL.md
│   └── summarize-thread/
│       └── SKILL.md
├── pyproject.toml         # uv-managed
└── README.md
```

The runtime starts by indexing all skills under `skills/` (loading just the frontmatter, per progressive disclosure). The firehose subscriber emits a `signal_received` event into the bus on each new signal commit; subscribed skills evaluate their `swarm.activation.*` metadata against the event and run if matched. Skill execution loads the body, invokes the LLM via the gateway-routed client (with tool-use access to "available agents" derived from profile records), and publishes any resulting signals. Self-DID dedup prevents the swarm agent from acting on its own posts.

The runtime's own DID is `swarm-agent.test` (per-skill DIDs would give better attribution but are a later decision; for now everything is signed by the runtime's identity).

### What this implies for nearer-term work

- **Phase 4 is structured around this design** — see the rewritten Phase 4 above for the build order.
- **The bridge needs a small refactor** (not zero, as I claimed in an earlier version of this doc): mention parsing, mention dispatch, and conversation context threading. Capability matching gets ripped out. Net change is bridge code shrinks. The "no bridge changes" claim was wrong — capability tags going away is a bridge change too, just a subtractive one.
- **Runtime configuration lives in `SKILL.md`'s `metadata` field**, not in a sidecar file. The spec's optional `scripts/`, `references/`, and `assets/` directories remain available for skills that need bundled resources; we just don't use them for runtime config.
