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

## Phase 4: Human onramp and the first swarm agent

**Goal:** Humans post natural-language signals (no capability tags), and the right things happen — without putting any content-aware logic into the bridge.

### Background: why this phase changed shape

The original Phase 4 had "implement the slow path in the bridge" as item one — a triage function inside the bridge that classified tagless signals and republished them with inferred capabilities. We rejected that approach. Two reasons:

1. **Humans never tag.** Realistic human posts ("where's package 123", "weird latency on checkout", "deploying in 30 min") don't carry capability tags. So *every* human signal needs triage, not just exceptional ones. There is no "fast path vs slow path" — there's just one path, and it goes through some kind of classification.

2. **The bridge must not be content-aware.** The moment the bridge looks at signal content to make routing decisions, it stops being a structural router and starts being a domain-aware service. That's the same architectural smell we removed when we pulled AgentGateway out of the dispatch path. The bridge knows about *structure* (capability tags, thread refs, DIDs). It must not know about *content*.

The resolution: triage isn't a bridge function at all. It's an agent — specifically, a **swarm agent** that subscribes to the firehose, sees tagless signals as they appear, classifies them, and publishes a tagged signal in the same thread. The bridge then dispatches the tagged signal through its existing fast-path logic. The bridge gets *zero* new code for any of this.

This generalizes: triage is one of a class of "swarm skills" that operate on swarm artifacts. See the **Swarm agents as a skill runtime** section below for the full design.

### Two classes of agents

Established as a hard architectural principle going forward:

- **Domain agents** — protocol-naive, A2A-only. They do work (deliver, look up, calendar, code search). They never see atproto. Today's delivery agent is the canonical example. Adoption story: any team with an A2A endpoint can plug in.
- **Swarm agents** — atproto-aware, firehose-subscribing. They decide what work should happen (triage, routing, summarization, dedup, correlation, severity tagging). They participate *in* the swarm rather than being dispatched *to*. These are intentional infrastructure built by whoever runs the swarm; they trade protocol-naivety for cross-cutting visibility.

The bridge's job stays exactly what it is: capability-tagged signal in → matching domain agents dispatched. Nothing more.

### Work

1. **CLI for humans.** Replaces the curl ceremony in the runbook. Optimized for fast capture, free-text first, terse output, retrievable threads. This is how humans actually post — and now that triage is going to live in a swarm agent, the human-posting flow is the prerequisite for the swarm agent to be useful, not the other way around.

2. **Swarm-agent runtime.** New service (likely a separate Go binary, or a small Python service if we want to lean on the LLM ecosystem). Reuses the bridge's firehose subscription code (extract a small shared package). Loads skills from a directory at startup. Subscribes to the firehose, decides per-event which (if any) skills to activate, publishes resulting signals back to the PDS under its own DID. Knows nothing about specific skills — it's a host, not a classifier.

3. **Triage skill.** First skill, validates the runtime end to end. Activation: any signal from a human DID that has no capability tags. Action: classify the content (LLM call or hand-written rules), publish a routing signal in the same thread carrying the inferred tags. The bridge then dispatches via its existing fast-path. The thread reads as a clear trace: human ask → triage decision → domain agent answer.

4. **Mentions as direct dispatch.** `@agent-name.test` in a signal should route directly to that agent, bypassing capability matching. This stays in the bridge (it's structural — handle resolution and direct dispatch, no content reasoning). Useful for the human-posting flow when you *do* know who to ask.

5. **A second swarm skill** to validate the runtime is genuinely skill-pluggable. Probably summarization (when a thread crosses N replies, post a checkpoint summary) — exercises a different activation pattern (thread state, not single event) and is genuinely useful.

6. **Multi-agent scenarios.** Post signals that match multiple domain agents. Observe how "dispatch to all" works in practice. Now also exercises swarm-skill-plus-domain-agent threads.

### What We'll Discover

- Whether autonomous LLM-driven activation (every firehose event = "is this skill relevant?") is cost-tractable, or whether we need cheap pre-filters via skill metadata
- How the routing-via-published-signal pattern feels in practice (clarity vs. noise in the thread)
- Whether the runtime needs explicit loop protection beyond own-DID dedup (e.g., a `triagedFrom` field on routing signals so triage doesn't classify its own output)
- How threads with multiple swarm skills firing on the same signal compose
- Whether the agentskills.io spec's progressive-disclosure model fits autonomous use as cleanly as it fits interactive coding agents

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

The bridge is structurally dumb: it knows about capability tags and thread refs, never about content. Anything that requires reading what a signal *says* — triage, classification, summarization, dedup, correlation, severity tagging — lives outside the bridge.

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

A skill is a directory containing at minimum a `SKILL.md` file (YAML frontmatter + Markdown body), with optional `scripts/`, `references/`, `assets/` subdirectories. Required frontmatter fields: `name` (1-64 chars, lowercase + hyphens, must match parent dir) and `description` (1-1024 chars, what the skill does + when to use it). Optional: `license`, `compatibility`, `metadata` (arbitrary key-value), `allowed-tools` (experimental).

The spec assumes **progressive disclosure**: metadata loads at startup for all skills (~100 tokens each), the full SKILL.md body loads only when a skill is activated, and resources load on demand. This is the model that lets a host scale to many skills without paying their context cost up front.

#### Where we extend the spec (carefully)

The spec was designed for **interactive LLM agent hosts** — tools where a user gives the agent a task, the agent looks at skill descriptions, decides which to load, then proceeds. Activation is implicit in the description ("use this skill when the user asks about PDFs").

Our use is **autonomous event processing**: every firehose event is a potential trigger, and we can't afford to do full LLM reasoning ("which skills are relevant?") on every commit message. We need cheap pre-filtering.

The spec accommodates this through the `metadata` field, which is explicitly an arbitrary map for client-specific extensions. We'd define a small set of conventional keys under a swarm-namespaced prefix:

```yaml
---
name: triage
description: Classify untagged signals from human users and publish a routing signal in the same thread with inferred capability tags. Use when a signal has no capability tags and originates from a human DID.
metadata:
  swarm.activation.collection: community.swarm.signal
  swarm.activation.condition: no-capabilities
  swarm.activation.sender-class: human
---
```

The runtime checks the `swarm.activation.*` metadata cheaply (no LLM call) on every firehose event and only loads the skill body and invokes the LLM for events that pass the prefilter. Skills without `swarm.activation.*` metadata fall back to spec-default behavior: the runtime asks the LLM "is this skill relevant?" using the description.

The extension lives entirely in the `metadata` field, so skills remain spec-conformant. Other agentskills-compatible hosts that don't understand `swarm.activation.*` would simply ignore it and use the description-based activation path.

### Routing as published signals (the thread is the trace)

The triage skill (and any other routing-style skill) doesn't return its decision through some private channel. It **publishes a signal** in the same thread carrying the inferred capabilities. The bridge sees that signal on the firehose and dispatches via its existing fast path. The thread becomes a self-documenting trace of "human asked → triage classified → domain agent answered," all signed and durable.

This means the bridge needs no special handling for swarm-skill output. Routing signals are just signals with capabilities. The fact that a swarm skill produced them is invisible to the bridge — and that's the goal.

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
├── cmd/swarm-agent/
│   ├── main.go            # firehose subscription, dispatch loop
│   ├── skills.go          # skill loader, metadata index, activation
│   ├── llm.go             # LLM client (Anthropic SDK)
│   └── publish.go         # PDS client for publishing signals
├── skills/
│   ├── triage/
│   │   ├── SKILL.md
│   │   └── references/
│   │       └── capability-taxonomy.md
│   └── summarize-thread/
│       └── SKILL.md
└── go.mod
```

The runtime starts by indexing all skills under `skills/` (loading just the frontmatter, per progressive disclosure). For each firehose event, it checks the metadata pre-filters against the event, collects matching skills, loads their bodies, invokes the LLM with the event content + skill instructions, and publishes whatever signal(s) the LLM produces. The LLM is the thing doing the actual reasoning; the runtime is the plumbing.

The runtime's own DID would be something like `swarm-agent.test` (or per-skill DIDs if we want per-skill attribution — that's a later decision).

### What this implies for nearer-term work

- **Phase 4 is now structured around this design** — see the rewritten Phase 4 above for the build order.
- **No bridge changes are required** for any of this. The bridge stays exactly as it is. That's the cleanest possible outcome and it's the clearest sign the architecture is right.
- **The skill format constraint affects what we build first.** Before writing the triage skill, we should pin a small number of capability tags as the canonical taxonomy (delivery, monitoring, calendar, etc.) so triage has something to classify *into*. Otherwise the first triage skill has to also invent the vocabulary, which conflates two different decisions.
