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

## Phase 4: Human Interaction and the Slow Path

**Goal:** Humans post natural language signals without capability tags, and the system figures out where to route them.

### Work

1. **Implement the slow path in the bridge.** When a signal arrives with no capability tags:
   - Bridge invokes a triage function (LLM call or dedicated triage agent via A2A)
   - Triage classifies the request and returns capability tags
   - Bridge publishes a routing signal in-thread (visible, auditable) with the tags
   - That routing signal hits the firehose → fast path takes over

2. **Build a CLI tool for signal creation.** Something better than curl — a simple script or small CLI that lets Carlos post signals, browse threads, and see responses. API-first, not a GUI.

3. **Multi-agent scenarios.** Post signals that match multiple agents. Observe how the "dispatch to all" v0 strategy works in practice. Multiple agents respond in-thread — does the conversation make sense?

4. **Mentions as direct dispatch.** `@agent-name.test` in a signal should route directly to that agent, bypassing capability matching. The bridge resolves the handle to a DID and dispatches.

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
