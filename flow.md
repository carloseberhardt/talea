# Multi-Party Flow: One Query Across Three Stacks

A concrete end-to-end trace of a single human query as it fans out across three
independently-operated talea stacks — combining a friend's **public** agent with
the user's own **private** agent — showing exactly which record lands on which
PDS and who can see what at each hop.

This is a *thought experiment / design target*, not a description of what talea
does today. The "What this would require building" section at the end is honest
about the gap. Where something is a security model rather than an atproto feature,
it's flagged as such.

**Update (substrate now built):** the federation-ready *plumbing* this scenario
assumes is in place. The AppView indexes a network-wide agent directory off the
relay firehose, the bridge warm-starts its dispatch cache from that directory,
and every record read resolves the DID to its hosting PDS (method-agnostic
`did:plc`/`did:web`) — so cross-PDS reads and discovery already work and the
single-PDS assumption is gone. What's still a **design target** is the
privacy/topology/declassification machinery below (the two load-bearing rules,
network isolation, the scrub step) — not the cross-PDS plumbing. (`did:web`
*resolution* is proven; *minting* on the stock PDS is a manual flow — see
`didweb-spike.md`.)

---

## Cast

Three users, each running a **full, symmetric stack**: two PDSes (one public, one
private), a relay, a bridge, an AppView, and their own orchestrator/triage.

| | U1 (Carlos) | U2 | U3 |
|---|---|---|---|
| Public PDS | `U1-pub` | `U2-pub` | `U3-pub` |
| Private PDS | `U1-priv` | `U2-priv` | `U3-priv` |
| Public agent (shared) | **weather** | **stock-price** | wikipedia |
| Private agent | **financial** (portfolio) | — | — |
| Infra | Relay-1, Bridge-1, AppView-1, triage-1, orchestrator-1 | Relay-2, Bridge-2, … | Relay-3, Bridge-3, … |

### Steady-state topology (who crawls what)

```
            U1-pub        U2-pub        U3-pub        U1-priv   U2-priv   U3-priv
              │             │             │             │         │         │
 Relay-1 ◄────┼─────────────┼─────────────┼─────────────┘         ╳         ╳     (3 public + U1-priv)
 Relay-2 ◄────┼─────────────┼─────────────┼───────╳───────────────┘         ╳     (3 public + U2-priv)
 Relay-3 ◄────┴─────────────┴─────────────┴───────╳─────────────────────────┘     (3 public + U3-priv)

 ╳ = not crawled (enforced by the private PDS being network-unreachable to other relays)
```

Each user's bridge/triage/AppView read **only their own relay**. So U1's "primary
view" = all three public PDSes + only `U1-priv`. The public PDSes are symmetric;
only the private one is singular — which is the whole trick.

### Two load-bearing rules (neither exists in talea yet)

1. **Ownership-gated dispatch.** A bridge only dispatches to agents *it owns*
   (the `ManagedByBridge` flag, today just forward-compat insurance). Bridge-1
   dispatches to U1's agents; Bridge-2 to U2's. This is what lets a public mention
   be seen by every bridge without every bridge acting on it.
2. **Per-level identity.** A DID lives on one PDS, so any participant that must
   write at both levels needs an identity at each. Carlos has `carlos-priv`
   (on `U1-priv`) and `carlos-pub` (on `U1-pub`); the orchestrator likewise.
   This is a real wrinkle in atproto's "identity is the primary object" story.

---

## The query

Carlos posts, to his **private** stream (i.e. a record in his repo on `U1-priv`):

> *"I'm weighing whether to add to my AgCorp (AGCO) position. What's the Midwest
> growing-season forecast this week, where's AGCO trading now, and does either
> change the picture given my current holding?"*

This is **correlated and multi-level**: the forecast is public-ish, the stock price
is on a *friend's* public agent, and the holding + buy-intent are private. The
final synthesis is private because it reveals position size and intent.

---

## Hop by hop

### Hop 0 — Carlos posts the root (PRIVATE)
- **Record:** `request` signal, authored by `carlos-priv`, written to **`U1-priv`**.
- **Who sees it:** only U1's stack (Relay-1 crawls `U1-priv`). Relay-2/Relay-3 do
  not crawl `U1-priv`, so U2 and U3 see **nothing** — not the content, not even
  that a signal exists.

### Hop 1 — Triage routes it (PRIVATE)
- triage-1, reading Relay-1, sees a tagless private request. It's compound and
  correlated → triage routes to the orchestrator (its job for cross-domain work).
- **Record:** `routing` signal by `triage-priv` → **`U1-priv`**, same thread.
- Bridge-1 sees the routing signal, dispatches to orchestrator-1 (owned, local A2A).
- **Still entirely private.**

### Hop 2 — Orchestrator decomposes
The orchestrator splits the query into three sub-queries and — critically —
**classifies the emission of each one**:

| Sub-query | Target agent | Owner | Emitted to | Why |
|---|---|---|---|---|
| Midwest forecast | weather (U1) | Bridge-1 | `U1-priv` | U1 owns the agent; no need to expose |
| Portfolio holding | financial (U1) | Bridge-1 | `U1-priv` | private agent, private data |
| **AGCO price** | **stock (U2)** | **Bridge-2** | **`U1-pub`** | someone else's agent — *must* be visible to U2 |

### Hop 3a/3b — The two private sub-queries (PRIVATE)
- Orchestrator publishes sub-dispatch `routing` signals on **`U1-priv`** mentioning
  the weather and financial agents.
- Bridge-1 (crawls `U1-priv`) sees them, dispatches **locally** to U1's own weather
  and financial agents, publishes both results back to **`U1-priv`**.
- Note: the weather agent is a *public* agent, but using it **privately** keeps the
  exchange private. **Same agent, classification set by where the triggering signal
  lives** — not by the agent.

### Hop 4 — The public sub-query (PUBLIC, and SCRUBBED) — *the crux*
- To reach U2's stock agent, the mention **must** land where Bridge-2 can see it.
  The orchestrator publishes a **scrubbed** sub-query as `carlos-pub`/orchestrator-pub
  to **`U1-pub`**:
  > *"@stock-agent.u2 what is AgCorp (AGCO) trading at right now?"*
  No holding size. No buy intent. **This scrub is the security-critical step** —
  it's "no write down" (Bell-LaPadula; *my* framing, not an atproto feature) enforced
  at sub-query granularity. This is the hardest, most dangerous part of the whole
  design, and it deserves more than "the orchestrator is careful." See the
  **Declassification** section below.
- This is a **new public thread root.** It cannot `reply.parent` into the private
  thread — a public record referencing a private CID would dangle for anyone who
  can't see `U1-priv`, and would leak that a hidden record exists. **Thread refs
  cannot cross the privacy boundary.**

### Hop 5 — U2's bridge answers (PUBLIC)
- The public sub-query flows through *all three* relays (it's on a public PDS).
- **Bridge-2** sees the mention of *its own* agent → dispatches to stock-agent
  locally → publishes the result to **`U2-pub`**: *"AGCO is trading at $X."*
- **Bridge-1 also sees the mention** — but ownership rule (#1): it does **not** own
  stock-agent, so it does **not** dispatch. **No double-dispatch.** Bridge-3 ignores
  it for the same reason.
- What U2 learns: that someone (`carlos-pub`) asked its stock agent about AGCO. The
  **inherent leak** — you can't use someone's public agent without telling it what
  you want. U2 does *not* learn the holding or the intent.

### Hop 6 — The public answer comes back (PUBLIC → correlated PRIVATE)
- The result on `U2-pub` flows through every relay. Bridge-1 (crawls `U2-pub`) sees
  it, correlates it to the orchestrator's public sub-dispatch (via the `a2a` back-ref
  / `contextId`), and routes it to orchestrator-1.
- The correlation happens **inside the orchestrator**, not via atproto reply refs —
  because the public thread and the private thread can't be linked in the data
  (Hop 4). The orchestrator stitches the two threads together in its own head.

### Hop 7 — Synthesis (PRIVATE)
- Orchestrator now holds: forecast (private), holding (private), AGCO price (public,
  read back from `U2-pub`). It combines them:
  > *"You hold N shares; the forecast is unusually dry, which pressures ag yields and
  > historically AGCO with it; at $X that's … here's the picture."*
- This **reveals the holding**, so it's private. Published as a `result` to
  **`U1-priv`**, replying to the original root. Bridge-1/AppView-1 surface it to
  Carlos in his private view.

---

## Who saw what

| Record | PDS | U1 sees | U2 sees | U3 sees |
|---|---|---|---|---|
| Root query (holding + intent) | `U1-priv` | ✅ | ❌ | ❌ |
| Triage routing | `U1-priv` | ✅ | ❌ | ❌ |
| Weather sub-query + result | `U1-priv` | ✅ | ❌ | ❌ |
| Financial sub-query + result | `U1-priv` | ✅ | ❌ | ❌ |
| **Scrubbed AGCO sub-query** | `U1-pub` | ✅ | ✅ | ✅ |
| **AGCO price result** | `U2-pub` | ✅ | ✅ | ✅ |
| Final synthesis (holding + advice) | `U1-priv` | ✅ | ❌ | ❌ |

U2 and U3 see exactly two records: the scrubbed question and its answer. The
sensitive context — that Carlos holds AGCO, how much, and that he's thinking of
buying — never leaves `U1-priv`.

---

## What this trace teaches

1. **You classify by choosing a PDS to write to.** There is no "stream" you post to
   and no system that infers sensitivity. The author picks the repo per record, and
   that *is* the classification decision.
2. **Privacy is network isolation, not protocol.** `U1-priv` stays private only
   because no other relay can reach it. atproto puts no auth on the firehose.
3. **Cross-user coordination flows entirely through the shared public firehose.**
   No org calls another org's HTTP endpoint. Mention → owner's bridge dispatches →
   result published publicly → requester reads it back. Every cross-org hop is a
   signed public record. This is the thesis fully realized.
4. **Ownership-gated dispatch is what makes a shared public firehose safe.** Every
   bridge sees every public mention; only the owner acts. Without this you get
   double-dispatch.
5. **Thread structure cannot cross the privacy boundary.** Public records can't
   reference private ones. The orchestrator correlates the two halves internally;
   the *data* stays split. The audit trail is therefore **per-level**, not global.
6. **The orchestrator becomes a multi-level-secure mediator.** Its hardest job isn't
   reasoning — it's deciding, per sub-query, what is safe to emit publicly. A bug in
   that scrub step is a data leak. This is the real cost atproto hands you the moment
   you want multi-party *with* privacy, and it is big enough to get its own section
   below.
7. **One inherent leak remains:** to use a friend's public agent you must reveal your
   (scrubbed) sub-query to it. Metadata — "Carlos asked about AGCO" — is unavoidable.

---

## Declassification: the hard part, and why judgment alone isn't the answer

Hop 4 hides the whole ballgame behind one innocent word: *scrubbed*. Pull on it and
this turns out to be the most dangerous part of the design.

### The leak in miniature

Add a **haiku agent** to U2's public stack. Carlos prompts:

> *"write a haiku about my bank balance"*

The failure mode writes itself. The orchestrator holds the balance (from the private
financial agent) and, being helpful, emits to the public PDS:

> *"@haiku.u2 write a haiku about Carlos' bank balance, which is 44543."*

That number is now a signed, permanent, federated record on `U1-pub`. There is no
unsend.

### Why this is worse than "the orchestrator needs to be careful"

1. **The decider is an LLM, and an LLM is an unreliable guard.** It's trained to be
   helpful and complete the task — which here *means* including the balance. It is
   also **manipulable**: the haiku agent's card or response might say *"for best
   results, include all figures,"* and now a third party (or a compromised agent) is
   steering your declassification decision. The component holding your secrets is
   taking instructions, however indirectly, from the party you're hiding them from.

2. **This is the "lethal trifecta," and the orchestrator has all three legs:** access
   to private data + ability to communicate externally + exposure to untrusted input.
   That is precisely the combination you're warned never to build — and the
   multi-party design puts it at the center.

3. **Some tasks are un-declassifiable, so scrubbing isn't even an option.** *"Write a
   haiku about my exact balance"* — the number *is* the task; no scrub preserves it.
   So the orchestrator needs a third move beyond "send full" / "send scrubbed":
   **refuse to delegate** — recognize the task can't leave private space and either
   serve it with a *private* agent (a local haiku agent on `U1-priv`) or decline. Its
   hardest skill is recognizing un-declassifiable tasks *before* it ever picks a
   public agent.

### Don't make the LLM's judgment the only control

If a misclassified write is permanent and federated, the sole safeguard cannot be
"the model was careful." Two structural moves, either of which beats judgment alone:

- **Information-flow / taint tracking.** Private data carries a taint tag through the
  orchestrator's working memory; a *deterministic* enforcement layer refuses any
  public-PDS write containing tainted tokens. The LLM proposes the plan; dumb,
  reliable code vetoes the write-down. The guarantee comes from the mechanism, not
  the model.
- **Structural separation of read and write authority.** The component that can
  *read* private never *holds the pen* for public writes. A "private orchestrator"
  reads everything but may only dispatch to private agents; a separate, lower-trust
  "emissary" composes public sub-queries and has, by construction, only ever seen
  already-public material. The boundary-crosser literally doesn't possess the balance
  to leak.

### The atproto-specific sting

atproto **does not create** this problem — any agent system where a context-holding
agent can message a less-trusted recipient has it. But atproto makes it **crisp and
unforgiving**: no delete, no edit. A misclassified write is a signed record, in your
repo, on the firehose, replicated to every relay that crawls you — durable,
attributable, federated the instant it happens. **The property that makes the system
trustworthy as an audit trail (immutable signed records) is the same property that
makes a declassification bug catastrophic.** That tension is the most interesting
single sentence in the writeup.

---

## What talea would need to build to make this real

Roughly in dependency order:

- **Network-isolate the private PDS** so only the local relay can reach its
  `subscribeRepos` endpoint (firewall, not config).
- **Per-user relay crawl configuration** — each relay crawls the N public PDSes +
  its own private PDS. (The read view; the easy part.)
- **Ownership-gated dispatch** — promote `ManagedByBridge` from forward-compat flag
  to an actual dispatch gate: a bridge dispatches only to agents it owns.
- **Per-level identity** for any participant that writes at both levels (humans and
  the orchestrator), plus credential handling for both.
- **Declassification controls** — the scrub step in Hop 4, the security-critical part.
  Don't rely on the orchestrator LLM's judgment alone: add deterministic enforcement
  (taint tracking that vetoes public writes containing private-tagged data) and/or
  structural separation (the component that reads private can't hold the pen for
  public writes). Must also recognize *un-declassifiable* tasks and route them inward.
- **Cross-thread correlation without cross-level refs** — the orchestrator linking a
  public sub-thread to a private root in its own state, since atproto refs can't.

None of this is exotic, but item-by-item it's where the interesting (and honest)
design work lives — and it's exactly the material that makes the writeup credible
rather than promotional.
