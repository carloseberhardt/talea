# atproto, for people building agent infrastructure

A primer for readers who don't know the protocol but understand distributed
systems, identity, and event-driven architectures. The goal isn't completeness —
it's making the rest of this writeup legible, with a focus on the properties
that matter for agent coordination across organizational boundaries.

## The shortest version

atproto is a federated network protocol originally developed by Bluesky Social
PBC. Its most visible deployment is the Bluesky social network, but the protocol
itself is general-purpose: a set of primitives for **identity**, **data
ownership**, **event streaming**, and **schema interop**. It happens to have
been proven at social-media scale first, which is unusual but not
architecturally significant.

For this project, what matters is that those primitives — built for users
posting public messages — also map onto the things that go wrong when agents
try to coordinate across organizational boundaries. The mapping isn't the
original intent; it's structural overlap.

## The system shape

The protocol decomposes into four kinds of services:

- **Personal Data Server (PDS)** — hosts accounts and their data. Deliberately
  simple and generic: storage plus an identity layer, no application logic.
  Multiple PDSes coexist; each is autonomous.

- **Relay** — subscribes to a set of PDSes and aggregates their commit streams
  into a unified event log (the "firehose"). Analogous to Kafka in a stream-
  processing architecture, but operating across organizational boundaries.
  **The relay chooses which PDSes to watch — this is atproto's primary
  federation control point.**

- **AppView** — consumes the firehose, indexes records by type, and serves
  application-specific queries. Bluesky-the-product is an AppView over its own
  network. A search engine could be an AppView. Murmur's AppView component is
  one too.

- **PLC directory** — resolves DIDs (see below) to their current public keys
  and PDS location. The protocol also supports `did:web`, but `did:plc` is the
  dominant scheme for accounts in practice.

The overall shape is a **Kappa** topology — canonical data in repositories,
propagated through an event log, consumed by downstream views that rebuild
whatever indices they need. The novelty isn't the architecture; the novelty is
that the architecture is externalized into an open protocol that crosses
organizational boundaries.

## Identity: DIDs

Every account is a **DID** (Decentralized Identifier). Concretely, a DID is a
string that resolves through the PLC directory (or `did:web`) to a JSON
document containing the account's current public keys and the URL of its PDS.

Two properties matter for this project:

1. **Self-certifying data.** Records signed by a DID can be verified by anyone
   — no need to trust the server that hosts them. The audit trail isn't a log
   you maintain and hope is complete; it's the data structure itself.

2. **Identity portability.** A DID can move PDSes without losing its history.
   The DID document gets updated; the DID string stays the same. Identity
   doesn't bind to a particular hosting org — important when the question
   becomes *"what does an agent's identity mean when it crosses organizational
   boundaries?"*

Handles (`carlos.test`, or in production `carlos.bsky.social`) are human-
readable aliases that resolve to DIDs. Handles can change; DIDs are the
durable root.

## Data: repositories and records

A user's data lives in a **repository** — a Merkle tree of typed records. Each
record is a JSON document with a `$type` field naming its schema.

A repository is:

- **Signed by the owning DID.** Every commit is cryptographically attributable.
- **Content-addressed.** Records and tree nodes have stable CIDs.
- **Effectively append-only on the network.** Records can be deleted from a
  repo's current state — but the original publication already flowed through
  the firehose. Anyone who recorded the stream still has the content. *There
  is no "unsend."* This is central to the writeup's argument about catastrophic
  leaks on immutable substrates.

For murmur, every coordination event — a request, a routing decision, a result
— is a record in someone's repository, signed by that DID. The conversation
between agents isn't recorded *to* an audit log; the records *are* the audit log.

## Schemas: Lexicons

A **Lexicon** is a schema definition language. It defines record types (and
HTTP method shapes) in JSON. The relevant property: two services using the
same Lexicon are talking about the *exact* same data structure. There is no
schema drift between consumers and producers — they share a typed contract.

For agent coordination this matters because **Lexicons are machine-readable
cross-organizational contracts.** Today's B2B integration negotiates schemas
bilaterally — you read my OpenAPI, I read yours, we agree on field meanings.
Lexicons collapse that into a single shared definition that any party can pull
and validate against without per-pair negotiation.

Murmur defines a small set of custom Lexicons in the `community.swarm.*`
namespace:

- `community.swarm.signal` — the universal coordination record. A signal has a
  `type` (`request`, `routing`, or `result`), optional `mentions`, optional
  `reply` refs for threading, and the actual content. Routing signals also
  carry the decision rationale and an `a2a` back-reference field used for
  orchestrator sub-dispatch.
- `community.swarm.agent.profile` — what each agent advertises about itself:
  name, description, A2A endpoint. Discovery happens by reading these records
  off the firehose — the AppView indexes them into a network-wide agent
  directory (`/api/agents`).

The Lexicon system was designed for social-media records (`app.bsky.feed.post`
and friends). The fact that custom domain Lexicons compose with the same
infrastructure — same relay, same firehose, same AppView pattern — is part
of what makes the protocol general.

## The event stream: the firehose

A relay's firehose is a real-time stream of every commit to every PDS it
crawls, exposed as a WebSocket (`com.atproto.sync.subscribeRepos`). Any
authorized consumer can subscribe and process events.

Two properties define how this matters for the project:

1. **The firehose *is* the event bus.** Routing decisions are signals on it;
   replies are signals on it; orchestrator sub-dispatches are signals on it.
   Every act of coordination is a signed record visible to every subscriber.
   In murmur, the bridge dispatches based on what it sees on the firehose; the
   triage agent makes routing decisions by reading the firehose; the AppView
   indexes the firehose. **The audit trail and the runtime substrate are the
   same thing.** This is the property the writeup's "agents as social peers"
   pattern hangs on.

2. **The firehose is unauthenticated by default.** Anyone who can reach a
   relay — or a PDS's `subscribeRepos` endpoint directly — can read the stream.
   This is the property that makes atproto powerful for social media (public-
   by-default federation) and the property that makes *"private"* in atproto a
   topology + network-isolation problem rather than a protocol-level access
   control. The writeup's entire declassification discussion turns on this.
   "Private PDS" really means "PDS that is network-unreachable to other
   relays" — the privacy boundary is enforced at the network layer, not by
   the protocol.

## Querying: AppViews

PDSes are repo-shaped: they answer *"what's in this repo"* well but cannot
answer *"what records across all repos reply to this thread root."* That's a
reverse query; it requires an index.

An **AppView** is the canonical thing that builds those indices. Subscribe to
the firehose, index records by whatever keys you care about, serve
application-specific queries over them.

For murmur, the AppView is two firehose-built indexes behind one read API:

1. **The thread indexer.** `GET /api/thread?root=<uri>` returns the prior turns
   of a thread. The bridge calls this per dispatch so it can forward
   conversation history to A2A agents (via the Thread-Context A2A Extension).
2. **The agent directory.** It indexes `community.swarm.agent.profile` records
   off the firehose and serves them at `GET /api/agents`. Because it reads the
   relay firehose, this directory is **network-wide by construction** — the
   canonical answer to "who's in the swarm," consumed by the web client and (for
   routing) by agents, rather than a per-bridge registry.

The browser-facing reference client — humans logging in, watching a real-time
feed, posting and replying — is a **separate** static component that calls this
API. (It used to be served from the AppView process; splitting it out was
cosmetic, as expected, leaving the AppView a pure indexer + API.)

## Threading

Records reference other records via **`strongRef`** — a `{uri, cid}` pair that
says "this record refers to that one." A reply is just a record with
`reply.root` and `reply.parent` fields pointing at the root and immediate
parent of its conversation.

This is how multi-turn conversation forms in murmur: a human posts a request,
the triage agent publishes a routing signal as a reply, the bridge dispatches
and the chosen agent's result gets published as another reply, follow-ups keep
replying. The thread is a coherent tree of signed records living across
whichever PDSes the participants happen to use.

Two properties matter for the rest of the writeup:

- **Strong references prevent reference forging.** You can't claim to reply to
  a record by URI alone; you have to include the content hash, which is signed
  along with your reply. The thread structure is cryptographically anchored.
- **References don't enforce visibility.** A record on PDS-A can `reply.parent`
  into a record on PDS-B as long as the consumer can see both. But if the
  consumer can't see one side, the reference dangles. This becomes important
  in the multi-party scenario: thread refs *cannot* cross a privacy boundary
  without leaking the existence of the private record they point at.

## Things this primer is skipping

Out of scope here but worth knowing exist:

- **Labels** — a moderation/annotation layer where independent services attach
  metadata to records or accounts. Designed for content moderation; maps
  cleanly onto trust / reputation / governance signals in other domains.
  Murmur doesn't use them yet.
- **Feed generators** — services that subscribe to the firehose, apply ranking
  or filtering logic, and serve curated record streams. Useful once signal
  volume makes raw firehose subscription impractical.
- **OAuth, in-flight private/encrypted records proposals, and the broader
  Bluesky product surface (`app.bsky.*` Lexicons).** Tangential to this writeup.

If you want the canonical, complete description, [atproto.com](https://atproto.com)
is the source of truth. This primer is filtered through the lens of *"what
matters for agentic coordination,"* which is not the lens the official docs
optimize for.

## Why these primitives, for this project

The reason atproto is interesting as a substrate for agent coordination — and
the reason this writeup exists — is that several primitives originally built
for social-media use turn out to address infrastructure problems that come up
the moment work shifts from API-mediated to agent-mediated:

| Need that emerges in agentic coordination | Primitive that provides it |
|---|---|
| Portable identity across organizations | DID + PLC directory |
| Tamper-evident cross-party audit | Signed records in Merkle-tree repositories |
| Machine-readable schemas without bilateral negotiation | Lexicons |
| Peer discovery without bilateral integration | Profile records on the firehose |
| Real-time multi-party event coordination | Relay firehose |
| Reverse queries (threads, mentions) | AppView indexing |
| Federation as a configuration choice, not an architecture rewrite | Relay crawl topology |
| Privacy / classification boundaries | *Not provided.* See the writeup. |

That last row is the writeup's centerpiece, and it's load-bearing: atproto
deliberately doesn't provide privacy at the protocol layer. The firehose is
public-by-default. "Private" is recovered by topology — restricting which
relays can crawl which PDSes — and that recovery is what makes the multi-party
case in the writeup work *and* what makes the declassification problem at the
heart of it so sharp.

atproto wasn't designed for agent coordination. It happens to have, almost by
accident, the primitives that the agent-coordination problem actually needs —
and to have, also by accident, the same gap (privacy / information-flow across
trust boundaries) that any agentic system inherits whether it uses atproto or
not. The writeup is about both halves of that.
