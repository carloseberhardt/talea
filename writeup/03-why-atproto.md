# Section 3 — Why atproto, specifically

> **Draft status:** first prose draft. Target ~1,050 words; runs slightly over
> because the identity-topology subsection carries the piece's answer to its
> most common objection. Compresses `atproto-primer.md` and the outline's §3.
> The full primer likely ships as an appendix or linked reference for readers
> who want the complete tour.

---

The list at the end of section 2 was assembled with no substrate in mind, and
one protocol already ships all of it. atproto was designed for federated
social media; its most visible deployment is Bluesky. Nothing about agents
appears anywhere in its design history. So the experiment is not "is atproto
the answer" — it is whether the mapping between these primitives and the
agent-coordination problem has structural weight, or is a coincidence of
feature lists. Sections 5 through 7 report the evidence. This section makes
the mapping precise.

## The primitives, through an agent-infrastructure lens

**Identity.** Every account is a DID, a durable identifier that resolves to a
document holding the account's current public keys and the location of its
data host. Two properties matter here. Records signed by a DID can be verified
by anyone, with no trust in the server that happens to host them — the data is
self-certifying. And identity is portable: an account can move hosts, the
document updates, the DID and its history stay intact. Identity does not bind
to a hosting organization, which is section 2's fourth requirement almost
verbatim.

**Data.** An account's data lives in a repository: a Merkle tree of typed
records, every commit signed by the owning DID, every node content-addressed.
On the network the repository is effectively append-only. A record can be
deleted from the repo's current state, but its publication already flowed
through the event stream, and anyone who recorded the stream still has it.
There is no unsend. Section 7 turns on this property.

**Schemas.** Record types are defined in Lexicons, a machine-readable schema
language. Two services using the same Lexicon share an exact typed contract —
there is no drift between producer and consumer, and no bilateral negotiation:
the definition is published once and any party validates against it. Custom
Lexicons (talea defines its own under a `community.swarm.*` namespace) ride
the same infrastructure as Bluesky's social records, which is part of what
makes the protocol general rather than a social-media appliance.

**The event stream.** A relay subscribes to a chosen set of data hosts and
merges their commit streams into one live event log, the firehose. Which
hosts a relay crawls is the protocol's federation control point, and it is
configuration, not architecture. The stream itself is unauthenticated by
design: anyone who can reach a relay can read everything it carries. That
choice is what makes public-by-default federation work, and it is what makes
"private" a topology problem — sections 6 and 7 are the bill for it.

**Indexes and threads.** Hosts answer "what is in this repo" but not "what
replies to this thread root" — reverse queries need an index, and the AppView
is the protocol's name for the service that builds one from the firehose.
Threading works by strong reference, a `{uri, cid}` pair: you cannot claim to
reply to a record without including its content hash, signed along with your
reply, so thread structure is cryptographically anchored. But references do
not enforce visibility. A reference to a record the reader cannot see simply
dangles — a small fact now, load-bearing in section 6.

Set section 2's requirements against these primitives and the coverage is
uncomfortably good:

| Need from section 2 | atproto primitive |
|---|---|
| Portable identity across orgs | DIDs and self-certifying records |
| Tamper-evident cross-party audit | Signed, content-addressed repositories |
| Schemas without bilateral negotiation | Lexicons |
| Discovery without bilateral integration | Profile records on the firehose |
| Multi-party event coordination | The relay firehose |
| Reverse queries (threads, mentions) | AppView indexing |
| Federation as configuration | Relay crawl topology |
| Privacy and classification boundaries | **Not provided** — sections 6 and 7 |

## Two layers, deliberately

atproto covers coordination: who exists, who said what to whom, in what
order, signed by whom. It says nothing about how an agent actually receives a
task and returns a result, and it shouldn't — that exchange is point-to-point
and already has a protocol. A2A gives agents a card describing their
capabilities, an endpoint, and a task lifecycle with typed messages.

Talea keeps the two layers strictly separate: every act of coordination is a
public signed record on the firehose, and the work itself moves over A2A
between exactly two endpoints. Collapsing them fails in both directions.
Task payloads on the firehose would make every execution detail a broadcast;
an A2A-only mesh would re-create bilateral B2B, with no shared audit trail
and no discovery. The split is also the adoption story: an agent that speaks
A2A can participate in the swarm without ever learning that atproto exists.
Section 4 shows the mechanism.

## Identity topology, and the objection that comes first

Anyone who knows atproto will raise it: the public network roots its
`did:plc` identities in a single directory that Bluesky operates, so doesn't
the whole substrate inherit a central point of control? Stated fairly, it
deserves a precise answer, because the answer turns into a selling point.

The global directory is a property of the public network, not the protocol.
atproto identity is "a DID that resolves to a DID document," and there are
two methods. `did:plc` resolves through a PLC directory — an append-only log
in which every operation is signed by the account's own rotation keys, so
the operator cannot forge or silently rewrite an identity. It is auditable
rather than authoritative-by-trust, it is open source, and it can be
self-hosted; `plc.directory` is Bluesky's instance, not the method. We run
our own in the sandbox. `did:web` involves no directory at all: the DID
document is served from `/.well-known/did.json` on a domain you control, and
identity roots in domain ownership — DNS plus the web PKI, the trust anchor
enterprises already federate on.

What defuses the objection completely is separating two audit layers that
are easy to conflate. **Identity-lifecycle audit** is the history of key
rotations, handle changes, and host migrations; that is what PLC's signed
log provides, and it is the only thing `did:web` gives up. **Data audit** —
who authored what, when, to whom — lives in the signed repository, carried
by the signing key in the DID document regardless of method. Talea's entire
audit story is the second layer, and it is identical under both methods. The
audit guarantees that matter for agents do not depend on PLC at all.

Which method fits is a per-deployment decision, and it turns on a single
axis: whether a single identity authority that everyone in scope already
accepts exists.

| Scope | Accepted single authority? | Default | Why |
|---|---|---|---|
| Single org (finance, audit, investigation) | Yes — the org itself | Self-hosted PLC | Reuses the org's existing trust root; lifecycle audit and key recovery are regulatory assets |
| Cross-org, sovereignty-first | No | `did:web` | No shared operator to govern; rides DNS and web PKI, which already span orgs |
| Cross-org, shared-audit-first | Yes — a consortium or regulator | Consortium PLC | Shared tamper-evident lifecycle log, at the cost of governing shared infrastructure |

The residual `did:web` risk is worth naming: identity is only as strong as
domain control, and a lapsed domain or DNS hijack compromises it with no
rotation-key recovery — which is itself an argument for PLC in the
highest-stakes single-org case. But all three rows share the same data-layer
audit trail, and all three resolve through one method-agnostic DID→PDS
resolver. Identity topology is a per-deployment toggle, not three
architectures.

That last claim is built, not just argued. The resolver runs behind every
record read in the current system, and `did:web` resolution is proven end to
end — HTTPS to DID document to hosting PDS. The one part that is not turnkey
is *minting* a `did:web` account on the stock PDS, which today is a manual
multi-step flow. Resolution is free; minting is the manual part.

---

### Citation stubs (format for venue later)

- atproto protocol documentation — <https://atproto.com> *(canonical spec
  reference for DIDs, repositories, Lexicons, firehose, AppViews)*
- *did:plc Specification v0.1* ("Public Ledger of Credentials") —
  <https://web.plc.directory/spec/v0.1/did-plc>; reference implementation at
  <https://github.com/did-method-plc/did-method-plc>
- *Agent2Agent (A2A) Protocol Specification* —
  <https://a2a-protocol.org/latest/specification/>. Originated by Google
  (announced April 2025), transferred to the Linux Foundation (June 2025);
  the sandbox targets the 1.0 spec (PascalCase method names).
- `did:web` provisioning findings — internal spike notes (`didweb-spike.md`);
  upstream issue bluesky-social/atproto#2100
