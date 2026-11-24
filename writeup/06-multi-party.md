# Section 6 — Three users, two privacy levels

> **Draft status:** first prose draft. Target ~1,400 words. Compresses
> `flow.md` (setup, trace, who-saw-what, takeaways); the declassification
> material stays in section 7, and the "what talea would need to build" list
> lands in section 8's open problems. Assumes [Figure 3: three-user topology]
> near the setup.

---

Everything before this point is built and running. This section is a design
exercise on top of it, and it is labeled as one. The cross-PDS plumbing the
scenario needs — the DID-to-PDS resolver, the network-wide directory — exists
in the code today. The privacy machinery — the isolation, the dispatch rule,
the scrub step — is a design target, worked out on paper because section 5's
meta-finding says the multi-party case is where the substrate earns its keep,
and a sandbox with one operator cannot reach it.

## The setup

Three users, each running the full stack from section 4, symmetrically: a
relay, a bridge, an AppView, their own triage and orchestrator — and two
PDSes each, one public and one private. Each user shares one public agent
with the others and keeps whatever private agents they like. Call the users
U1 (Carlos, who runs a public weather agent and a private financial agent
that knows his portfolio), U2 (a public stock-price agent), and U3 (a public
wikipedia agent).

The topology is the whole trick. Each user's relay crawls all three public
PDSes plus *their own* private PDS, and nobody else's. From any seat the
view is the same shape: everything public, plus exactly one private stream.
And the enforcement is not a protocol feature. Recall from section 3 that
the firehose is unauthenticated: `U1-priv` stays private only because it is
network-unreachable from Relay-2 and Relay-3 — a firewall rule, not an
access-control list.

Two rules carry the design, and neither exists in talea today:

1. **Ownership-gated dispatch.** A bridge dispatches only to agents it owns.
   Every bridge sees every public mention; only the owner's bridge acts.
2. **Per-level identity.** A DID lives on one PDS, so anyone who writes at
   both levels needs an identity at each: `carlos-priv` on the private PDS,
   `carlos-pub` on the public one, and likewise for the orchestrator. This
   is a real wrinkle in atproto's identity-is-primary story, and it goes on
   section 8's open-problems list.

## The query

Carlos posts to his private stream — that is, authors a record in his repo
on `U1-priv`:

> *"I'm weighing whether to add to my AgCorp (AGCO) position. What's the
> Midwest growing-season forecast this week, where's AGCO trading now, and
> does either change the picture given my current holding?"*

The query is multi-level and correlated. The forecast is public-ish, the
stock price lives behind a *friend's* public agent, and the holding and the
buy intent are private — as is the synthesis, because it reveals both.

## The trace

**Hops 0–1, private.** The request lands on `U1-priv`; only Relay-1 crawls
it, so U2 and U3 see nothing — not the content, and not even that a signal
exists. Triage-1 routes it to the orchestrator with a routing signal on the
same PDS. Everything so far is invisible outside U1's stack.

**Hop 2, the decomposition.** The orchestrator splits the query three ways,
and — this is the crux of the whole section — classifies the *emission* of
each sub-query:

| Sub-query | Target agent | Owned by | Emitted to | Why |
|---|---|---|---|---|
| Midwest forecast | weather (U1) | Bridge-1 | `U1-priv` | own agent; no reason to expose |
| Portfolio holding | financial (U1) | Bridge-1 | `U1-priv` | private agent, private data |
| AGCO price | stock (U2) | Bridge-2 | `U1-pub` | someone else's agent — must be visible to U2 |

**Hop 3, private sub-queries.** Bridge-1 sees the two private dispatches on
its relay and dispatches locally; both results come back to `U1-priv`. Note
what happened with the weather agent: it is a *public* agent, used
*privately*. Classification is set by where the triggering signal lives, not
by the agent. The same agent serves both levels without knowing which one it
is on.

**Hop 4, the public sub-query — scrubbed.** To reach U2's stock agent, the
mention must land where Bridge-2 can see it. The orchestrator publishes, as
its public identity, to `U1-pub`:

> *"@stock-agent.u2 what is AgCorp (AGCO) trading at right now?"*

No holding size, no buy intent. This scrub is the security-critical step in
the entire design — a "no write down" rule, in the Bell–LaPadula sense,
enforced at sub-query granularity. We flag clearly: that is our security
framing laid on top of atproto, not anything the protocol provides. Notice
also that this is a *new public thread root*. It cannot reply into the
private thread, because a public record referencing a private one would
dangle for every reader who cannot see `U1-priv` — and would leak that a
hidden record exists. Thread references cannot cross the privacy boundary.

**Hop 5, the answer.** The public sub-query flows through all three relays.
Bridge-2 owns the stock agent, dispatches, and publishes the result to
`U2-pub`: AGCO's price. Bridge-1 and Bridge-3 both saw the same mention and
did nothing — rule 1 — which is what makes a shared firehose safe from
double-dispatch. What U2 learns is that `carlos-pub` asked about AGCO. That
is the inherent leak: you cannot use someone's public agent without telling
it what you want. What U2 does not learn is the holding or the intent.

**Hops 6–7, correlation and synthesis.** The result on `U2-pub` reaches
Relay-1; Bridge-1 correlates it to the orchestrator's public sub-dispatch by
its back-reference and routes it to orchestrator-1. The correlation happens
*inside the orchestrator* — the public thread and the private thread cannot
be linked in the data, so the orchestrator stitches the halves together in
its own state. It now holds forecast, holding, and price, and the synthesis
("you hold N shares; the forecast is dry; at this price, here's the
picture") reveals the position — so it is published to `U1-priv`, replying
to the original root.

## Who saw what

| Record | Lives on | U1 | U2 | U3 |
|---|---|---|---|---|
| Root query (holding + intent) | `U1-priv` | ✅ | ❌ | ❌ |
| Triage routing | `U1-priv` | ✅ | ❌ | ❌ |
| Weather sub-query + result | `U1-priv` | ✅ | ❌ | ❌ |
| Financial sub-query + result | `U1-priv` | ✅ | ❌ | ❌ |
| Scrubbed AGCO sub-query | `U1-pub` | ✅ | ✅ | ✅ |
| AGCO price result | `U2-pub` | ✅ | ✅ | ✅ |
| Final synthesis (holding + advice) | `U1-priv` | ✅ | ❌ | ❌ |

U2 and U3 see exactly two records: a scrubbed question and its public
answer. The sensitive context never left `U1-priv`.

## What the trace teaches

1. **Classification is an authoring decision.** There is no stream you post
   to and no system that infers sensitivity. The author picks which repo a
   record lands in, per record, and that choice *is* the classification.
2. **Privacy is network isolation, not protocol.** The private PDS stays
   private because other relays cannot reach it. Nothing in atproto is doing
   the protecting.
3. **Cross-user coordination flows entirely through the shared public
   firehose.** No org calls another org's HTTP endpoint. Mention, dispatch
   by the owner, result published publicly, read back by the requester —
   every cross-org hop is a signed public record. This is section 2's
   requirements list operating as designed.
4. **Ownership-gated dispatch is what makes a shared firehose safe.** Every
   bridge sees every public mention; exactly one acts.
5. **Thread structure cannot cross the privacy boundary.** Public records
   cannot reference private ones, so the orchestrator correlates the halves
   internally and the audit trail is per-level, not global. A complete
   cross-level reconstruction of "what happened" exists only inside the
   orchestrator's head.

There is a sixth lesson, and it is big enough to take the next section. The
orchestrator in this trace has quietly become a multi-level-secure mediator.
Its hardest job was never the reasoning — it was deciding, sub-query by
sub-query, what was safe to say in public. In this trace it decided well.
The next section is about why "it decided well" cannot be the plan.

---

### Citation stubs (format for venue later)

- Bell–LaPadula reference for the "no write down" framing: D. E. Bell and
  L. J. LaPadula, *Secure Computer Systems: Mathematical Foundations*, MITRE
  Corporation, MTR-2547 Vol. I (ESD-TR-73-278-I), 1973 — shared with
  section 7's [BL73]; consolidate at assembly.
