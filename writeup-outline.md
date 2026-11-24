# Talea Writeup — Outline

This is a working outline, not a draft. The point is to see the shape and find
out where it bends before writing prose. Sections are sized in rough words to
keep the whole piece around 5,000–8,000 — long enough for the argument to
breathe, short enough to actually finish.

## Title options

- *When agents do the work, the wires have to change* — provocative, frames it as forward-looking infrastructure essay
- *Talea: agents as social peers on a federated substrate* — descriptive, signals it's a case study
- *What an agent-native B2B substrate might look like (and where it bends)* — the most honest about the speculation
- *The substrate problem in agent-mediated work* — academic-y, signals "argument piece"

Probably: a provocative main title + descriptive subtitle. Something like
*"When agents do the work, the wires have to change — a case study on atproto."*

## The framing the whole piece serves

> *As cross-org and cross-domain work shifts from API-mediated to agent-mediated,
> several infrastructure problems currently solved bilaterally start needing a
> shared substrate: portable identity, signed cross-party audit, machine-readable
> schemas, peer discovery. Here's what an agent-native substrate for that world
> might look like, what we learned building one on atproto, where the substrate
> earns its keep, and where the genuinely hard problems live — particularly
> information-flow across classification boundaries, which the substrate explicitly
> doesn't solve for you.*

Two distinct audiences who should recognize themselves:
- **Forward-looking B2B-platform readers** — "this is where things are heading."
- **Present-day regulated-function / compliance readers** — "this maps onto Legal/HR/Finance/Audit cross-classification problems we have right now."

The discipline to hold: be clear about claim vs. speculation. *"Agent-mediated
B2B is coming"* = speculation, mark it. *"If it does, these properties become
load-bearing — and here's the structural reason"* = rigorous, make it teeth-first.

---

## 1. Opening (~400 words)

**Purpose:** Frame the question. Get the reader to care without leading with atproto or talea.

**Move:** Open on the shift. Today's B2B is API-mediated and the integration tax
is brutal because every pair negotiates everything bilaterally — schemas, auth,
audit, error handling. EDI is the proverbial cautionary tale: "what happens when
the network effect doesn't kick in because there's no shared substrate." When
agents do this work — not bots, not scripts, but identities with persistent
state, multi-turn conversations, and fluid participant sets — several things
break the API pattern at once. This essay is about what an agent-native
substrate for that world might need to look like, what we learned trying to
build one on atproto, and where the substrate explicitly doesn't help.

**Honesty move at the end of the opening:** flag the speculation. We're not
predicting B2B becomes agentic; we're asking "if it does, what would the
substrate underneath need to provide?"

## 2. What changes when agents do the work (~900 words)

**Purpose:** The structural argument. This is the spine the rest of the piece hangs off.

**The four shifts** (each gets a paragraph with a concrete example):
1. **Conversations replace transactions.** Multi-turn, context-bearing, with task threads and follow-ups. APIs are stateless request-response; bilateral logs don't reconcile cleanly when both sides hold partial state.
2. **Participant sets become fluid.** Agents fan out, discover each other, bring in additional agents mid-task. REST doesn't model that.
3. **Audit becomes load-bearing in a different shape.** Humans driving APIs: "what did the call do." Agents driving: "what did the agent decide, with what context, talking to whom, with who else seeing what." A multi-party trail, not a per-endpoint log.
4. **Identity needs to be portable.** OAuth tokens scoped to each side's directory are meaningless to a third party (auditor, regulator, partner). Cross-org agentic work needs identity that means the same thing in every system that can verify it.

**Section close:** These four shifts move several properties from optional to
load-bearing: federation without a central owner, signed cross-party audit,
machine-readable schemas, portable identity. The same properties that make
atproto interesting for social media — for entirely structural reasons.

## 3. Why atproto, specifically (~1050 words)

**Purpose:** Walk through atproto's primitives and the mapping. Borrow heavily from the existing README primer; it's already good.

**Key intellectual move:** atproto wasn't designed for this. It was designed for
federated social media. But its primitives — DIDs, signed repos, Lexicons, the
firehose, feed generators, labels, threading — happen to address exactly the
coordination problems agentic work surfaces. That's the experiment: not "is
atproto the answer" but "does the mapping have structural weight, or is it
coincidence."

**The two-layer model as a deliberate choice:** atproto for *coordination*
(broadcast, threading, identity, audit), A2A for *execution* (point-to-point
task delivery). Each does what it's good at; conflating them is bad.

**Material:** the README's primer + the architecture principles from
IMPLEMENTATION.md.

### Identity topology — and why "atproto is centralized on Bluesky" is wrong

**Purpose:** Pre-empt the most common objection (atproto identity = the global
`plc.directory`, ergo centralized) and turn it into a B2B selling point. This is
where the portable-identity shift from section 2 gets its concrete payoff.

**The objection, stated fairly:** atproto's public network roots `did:plc`
identities in a single directory Bluesky operates. A skeptic concludes the whole
substrate inherits that central point of control.

**Why it's wrong — the global PLC is a property of the *public network*, not the
protocol.** atproto identity is "a DID that resolves to a DID document," and
there are two methods:
- **`did:plc`** — resolves via a PLC directory. PLC is an append-only log where
  every operation is signed by the account's *rotation keys*, so the operator
  can't forge or silently rewrite identities — auditable, not authoritative-by-trust.
  And it's open source: *we already run our own in the sandbox.* The directory can
  be self-hosted; `plc.directory` is just Bluesky's instance of it.
- **`did:web`** — no central directory at all. The DID document lives at
  `https://<domain>/.well-known/did.json`; resolution is just HTTPS. Identity is
  rooted in **domain ownership** — which is exactly the trust anchor enterprises
  already federate on via DNS + the Web PKI.

**What audit actually rests on (the framing that defuses the objection completely).**
There are *two distinct audit layers*, and they must not be conflated:
1. **Identity-lifecycle audit** — the history of key rotations, handle changes,
   PDS migrations. This is what PLC's signed append-only log provides, and the
   *only* thing `did:web` gives up.
2. **Data / interaction audit** — who authored what, when, to whom. This lives in
   the **signed repository**: every commit is signed by the account's signing key
   (carried in the DID doc regardless of method), content-addressed by CID, and
   streamed over the firehose. Our entire agent-audit story — signed signals, A2A
   correlation refs, tamper-evident threads — is *this* layer, and it is
   **identical under `did:plc` and `did:web`.** The DID method never touches it.

So the sharp sentence for the piece: *the audit guarantees that matter for agents
don't depend on PLC at all.* Choosing `did:web` trades only the identity-lifecycle
log (and rotation-key recovery) — never the who-did-what trail.

**The decision matrix** (this maps directly onto section 8's two targets):

| Scope | Single accepted identity authority? | Default | Why |
|---|---|---|---|
| Single org (finance / audit / investigation) | Yes — the org | **PLC, self-hosted** | Reuses the org's existing central trust root; identity-lifecycle audit + key recovery is a regulatory asset exactly where it's needed |
| Cross-org, sovereignty-first | No | **`did:web`** | No shared operator to govern; rides DNS/Web PKI that already spans orgs; each org owns its identity *and* its domain risk |
| Cross-org, shared-audit-first | Yes — a governed consortium / regulator | **consortium PLC** | Shared, tamper-evident identity-lifecycle log across institutions — at the cost of governing shared infra |

The real axis isn't "single vs. cross org" — it's *whether a single identity
authority everyone in scope already accepts exists.* Single orgs trivially have
one; a steering committee is precisely the body that *could* operate a consortium
PLC if the cross-org use case needs shared lifecycle audit. The residual `did:web`
risk worth naming: identity is only as strong as domain control (a lapsed domain
or DNS hijack compromises it, with no rotation-key recovery) — which is itself an
argument for PLC in the highest-stakes single-org financial case.

**The substrate-agnostic kicker:** all three rows share the same data-layer audit
trail and resolve through one method-agnostic DID→PDS resolver. Identity topology
is a per-deployment *toggle*, not three architectures. That's what makes the
portable-identity claim from section 2 hold up under scrutiny.

**Built, not just argued:** the resolver is live behind every record read, and
`did:web` resolution is proven end-to-end (HTTPS → DID doc → PDS). The one thing
that *isn't* turnkey is `did:web` account **minting** on the stock PDS — it's a
manual `reserveSigningKey` → self-host `/.well-known/did.json` → `createAccount`
flow. The crisp line: **resolution is free; minting is the manual part.**

**Material:** this conversation's two-layer audit framing + decision matrix; the
built DID→PDS resolver (see §4) and the `didweb-spike.md` findings.

## 4. What we built (~1100 words)

**Purpose:** Make the architecture concrete. This is the case-study layer.

**Architecture sketch (with one good diagram):** PDS / Relay / AppView / Bridge / agents. Show the data flow.

**The principles that emerged from building it:**
- The bridge knows about structure, never about content. Mentions, thread refs, A2A correlation IDs — yes. What a signal *means* — no.
- Two classes of agents: **A2A-native** (protocol-naive, exposes A2A endpoint; everything except triage) and **atproto-native** (subscribes to firehose directly; today just triage). Most agents stay naive; infrastructure-adjacent agents opt in.
- The thread *is* the conversation. A2A tasks are units of work, not conversations. `contextId` is the grouping key.
- Coordination is published. Routing decisions are themselves signed records. The firehose is the event bus.
- Restart durability via the Thread-Context A2A Extension: atproto stays authoritative, agents reconstruct state from the signed thread.
- Federation-ready by construction (built in a dedicated pass). The AppView indexes the agent directory off the relay firehose — network-wide by construction, not a per-bridge registry — and serves it at `/api/agents`; the bridge warm-starts its dispatch cache from that directory; every record read resolves the DID to its hosting PDS via a method-agnostic resolver (`did:plc`/`did:web`); and the web client is a separate static component, leaving the AppView a pure indexer + API. Still single-instance, but the single-PDS assumption is gone — federation becomes config + a relay crawl, not a refactor.

**What's deliberately not in scope:** privacy/encryption (acknowledged limitation), feed generators (deferred), labelers / trust scoring (deferred), bridge HA (deferred), `did:web` account *minting* (resolution is built; minting on the stock PDS is a manual flow — see `didweb-spike.md`).

**Material:** the architecture from IMPLEMENTATION.md, the decisions list (especially the load-bearing ones: bridge-as-structure-only, A2A-native-vs-atproto-native, orchestrator-via-/publish).

## 5. What the implementation taught us (~1100 words)

**Purpose:** The honest findings. Not "everything worked" — *what surprised us.*

### The good news (short)
- The two-layer model holds. A2A-native agents really do participate without learning atproto.
- Mention dispatch, multi-turn via contextId, orchestrator decomposition via /publish — all work as designed.
- The thread is genuinely tamper-evident audit. "Who decided what when" is structural, not a log you maintain.
- Discovery via firehose-borne profile records works without a central registry — the AppView indexes them into a network-wide agent directory (`/api/agents`) that the bridge and web client both read.

### The meta-finding (the centerpiece of this section)
**The properties that make atproto compelling only become load-bearing at multi-party scale.** As a single user we paid all the protocol cost and received none of the structural benefit. The experiment is most informative about what it *can't* test — which is exactly the multi-party case where the substrate would matter. The README's own honesty bet was right.

### Things we got wrong and changed our minds about
- Originally treated triage as a regular A2A agent. Flipped to atproto-native after the bridge kept accumulating triage-specific special cases. Honest model: infrastructure-adjacent agents opt into atproto-native; everyone else stays naive.
- Originally let the bridge drive the orchestrator loop. Pulled it back to the orchestrator itself via /publish. The bridge is event-driven and stateless per signal; the orchestrator is the thing that thinks.
- Briefly put AgentGateway on the dispatch path. Removed it — second registry to keep in sync, bridge became a worse SPOF. Came back later in a different role (LLM router) where it doesn't conflict.
- Originally had the AppView *proxy the bridge's* registry for discovery, and the bridge enumerate a single PDS via `listRepos`. Reversed both: the relay-fed AppView is the network-wide directory, and the bridge warm-starts from it. The dependency had been inverted — a per-bridge registry can only ever see what one bridge learned; a relay-fed indexer sees the whole network. Discovery is core indexer work, not a bridge concern.

**Material:** the "Infrastructure Decisions Made" list in IMPLEMENTATION.md, especially the ones where we flipped.

## 6. The thought experiment: three users, two privacy levels (~1400 words)

**Purpose:** Take the multi-party scenario from flow.md and lay it out as the load-bearing case. This section pays off the framing in section 2.

### The setup
Three users, each running a full stack (two PDSes — public + private — plus relay, bridge, AppView, own orchestrator/triage). Each runs a public agent shared with the others, plus private agents not shared. Symmetric topology: from any seat, N public PDSes and exactly one private.

**Topology diagram:** the one from flow.md (each user's relay crawls 3 public + their own private).

### The query trace
The AGCO / financial / weather / stock-price example, end to end. Show:
- Which record lands on which PDS at each hop
- The "who saw what" table
- How cross-user coordination flows entirely through the shared public firehose
- The ownership-gated dispatch rule (each bridge dispatches only to its own agents) — what makes a shared firehose safe from double-dispatch

### The five things this teaches
(Lifted/tightened from flow.md takeaways)
1. Classification is an authoring decision (per-record PDS choice)
2. Privacy is network isolation, not protocol
3. Cross-user coordination flows through the shared public firehose; A2A dispatch stays local to each org
4. Ownership-gated dispatch is what makes a shared firehose safe
5. Thread refs can't cross the privacy boundary; correlation happens internally; audit is per-level, not global

**Material:** flow.md is essentially this section in seed form. Tighten the prose; drop the "what talea would need to build" list (move to section 8); keep the trace and the table.

## 7. The hard part: declassification (~1700 words) — *the centerpiece*

**Purpose:** Land the negative result hard. This is the most original finding in the whole writeup.

### The leak in miniature
Haiku agent example. Orchestrator holds the balance, is asked to *"write a haiku about my bank balance,"* and helpfully emits *"@haiku.u2 write a haiku about Carlos' bank balance, which is 44543."* That number is now a signed, permanent, federated record. There is no unsend.

### Why this is worse than "the orchestrator should be careful"
1. **The decider is an LLM, and an LLM is an unreliable guard.** Trained to be helpful and complete the task — which here means *including the balance*. Inconsistent withholder.
2. **Manipulability.** The haiku agent's card or response can steer the orchestrator's declassification decision. The component holding your secrets is taking instructions, however indirectly, from the party you're hiding them from.
3. **The lethal trifecta.** Access to private data + ability to communicate externally + exposure to untrusted input. The orchestrator has all three. This is precisely the combination security folks warn never to build — and the multi-party design puts it at the center.
4. **Some tasks are un-declassifiable.** *"Write a haiku about my exact balance"* — the number *is* the task; no scrub preserves it. The orchestrator needs a third move: refuse and route inward.

### Two structural mitigations (because judgment alone isn't the answer)
- **Information-flow / taint tracking.** Private data carries a taint tag through the orchestrator's working memory; a deterministic enforcement layer refuses any public write containing tainted tokens. The LLM proposes the plan; dumb, reliable code vetoes the write-down.
- **Structural separation of read and write authority.** The component that reads private never holds the pen for public writes. A "private orchestrator" reads everything but only dispatches privately; a separate "emissary" composes public sub-queries and has only ever seen public material. The boundary-crosser literally doesn't possess the balance to leak.

### The atproto-specific sting (the writeup's sharpest sentence)
atproto *doesn't create* this problem. But the property that makes it valuable
as an audit trail — immutable signed records — is exactly the property that
makes a declassification bug catastrophic. No edit, no delete, no oops. **The
audit-strength and the leak-catastrophe are the same property seen from two
sides.** That tension is the most interesting thing the experiment surfaced.

### What this implies for substrate design (generalize beyond atproto)
Any public-by-default federated substrate that hosts agentic coordination across
trust boundaries inherits this problem. The substrate doesn't solve it; the
substrate just shapes how it manifests. atproto's particular shape makes it
crisp and unforgiving. Choosing a different substrate doesn't make it go away.

**Material:** the entire "Declassification" section of flow.md, expanded for an
audience that hasn't read the rest of the piece. The haiku example is the
anchor.

## 8. Where this points (~700 words)

**Purpose:** Forward-looking close. Honest about what's claim vs. speculation vs. open problem.

### Two natural targets
- **Cross-org / agentic B2B** — speculative but structurally backed. The forward-looking case. Most enterprises wouldn't deploy this today; the question is what they'll want when the work shifts. *Identity topology here: `did:web` (sovereignty-first) or a consortium PLC if shared identity-lifecycle audit is needed — see the matrix in section 3.*
- **Cross-classification within a single org** — regulated functions (Legal, HR, Finance, Internal Audit). Present-day case. Information-flow constraints already exist; today they're solved by complete segregation, which is expensive. An agent-mediated cross-function workflow that's *structurally* multi-level-secure would be a real improvement. *Identity topology here: self-hosted PLC — the org is its own trust authority, and the lifecycle audit is a regulatory asset (section 3 matrix).*

Departmental federation as the most plausible deployment shape — each regulated
function runs its own stack, federates selectively, and the declassification
machinery enforces info-flow rules across the boundary.

### Open problems (i.e., things the writeup explicitly does NOT claim to solve)
- Declassification controls (taint tracking + structural separation — sketched, not built)
- Identity at multiple classification levels (the two-DID-per-human wrinkle)
- Federation at real scale — the *code* is now federation-ready (network-wide AppView directory + method-agnostic DID→PDS resolver; the single-PDS assumption is removed), but it's untested live. A real multi-org deployment still needs identity infra (shared PLC or `did:web`), real domains/TLS, and relay crawl config. `did:web` *resolution* is proven; `did:web` *minting* on the stock PDS is a manual flow (`didweb-spike.md`).
- Bridge HA / multi-instance dispatch dedup
- Encrypted / private records — out of scope for atproto core; an open question for the substrate

### What this experiment actually proves (the most useful sentence in this section)
That the **agents-as-social-peers** design pattern is implementable, and the
**two-layer (coordination / execution) split** holds at the architectural seam.
That part is substrate-agnostic. atproto is one possible substrate; the pattern
would work on others. The case study is evidence the pattern composes with a
federated public-by-default substrate; the declassification finding is what it
costs.

### Closing beats
- This is a research artifact, not a product proposal. The code is evidence; the writeup is the contribution.
- We learned more from what the substrate *doesn't* solve than from what it does.
- The right reader recognizes their own problem in either the cross-org or cross-classification framing and starts thinking about whether the shape fits.

---

## Questions to settle before writing prose

1. **One audience or two?** I've structured for both forward-looking B2B *and* present-day compliance readers. They want the same essay; they recognize themselves at different points. Is that right, or do you want to pick one as primary?
2. **How much prior atproto knowledge to assume?** Section 3 is short if the reader knows it; longer if not. Inclined to assume *some* familiarity — link to the README primer rather than rebuild it.
3. **Diagrams.** Plan for ~3: (a) the two-layer model (atproto coordination + A2A execution), (b) the talea architecture (PDS/relay/AppView/bridge/agents), (c) the three-user topology from flow.md. Worth investing in good ones; this kind of essay lives or dies on whether the reader can hold the picture.
4. **Voice.** *"We"* (the experimenter — natural for a research log) vs. *"I"* (closer to a personal essay). I'd default to *"we"* — feels right for the technical content even if it's just you.
5. **Length tolerance.** 5–8k words is a real essay. Do you want shorter (4k, blog-post tight) — would mean cutting depth in sections 5 and 7 and dropping section 3? Longer (10k+, full design doc) would let you go deeper on the declassification mitigations and add more multi-party scenarios.
6. **Publication shape.** Personal blog post? Submitted somewhere? Just a doc you hand to two friends? Different shapes want different framing energy.
7. **The provocative-vs-descriptive title** — which way does your gut go?

### Settled (2026-09-01)

- **Q5 length:** 5–8k as outlined.
- **Q6 publication shape:** submitting somewhere (venue TBD) — more self-contained
  background, hedged claims; leans descriptive on the title (Q7 still open).
- **Scope:** patent material is out.
- **Drafting: all eight sections have first prose drafts in `writeup/`**
  (01-opening through 08-where-this-points). Citations verified for the
  lethal trifecta (Willison, Jun 2025) and Bluesky's off-protocol DMs
  (TechCrunch / anderegg.ca / 2024 protocol roadmap). **Citation pass done (2026-09-01):** A2A spec
  (a2a-protocol.org; Google → Linux Foundation), did:plc spec v0.1
  (web.plc.directory), Bell–LaPadula (MTR-2547 Vol. I / ESD-TR-73-278-I,
  1973), EDI (Iacovou/Benbasat/Dexter, MISQ 1995), and the roadmap's exact
  sentence — now quoted verbatim in §7 ("…the repository abstraction, which
  is only used for public content"; canonical URL is atproto.com, the
  docs.bsky.app URL redirects). Still owed: the three figures, assembly
  into one file, title decision (Q7), and an edit-pass once content settles.

## What I'd write first

I'd draft **section 7 (declassification) first**, because:
- It's the centerpiece of the contribution
- It's the most original material (mostly already in flow.md, ready to expand)
- If it doesn't land at essay length, the whole framing is in trouble and we want to know early
- Once it works, the rest of the piece serves it

Section 2 (the structural argument) second — because it's the spine and everything else has to be consistent with it.

Sections 1 and 8 (opening + close) last — they're framing; they work best after we know what they're framing.
