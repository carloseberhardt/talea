# Section 4 — What we built

> **Draft status:** first prose draft. Target ~1,100 words. Compresses the
> current-state and principles material from `IMPLEMENTATION.md`. Assumes
> [Figure 2: murmur architecture] lands near the top — PDS / relay / AppView /
> bridge / agents with the data flow.

---

Murmur is small on purpose: five infrastructure services and a handful of
agents, running as a sandbox on one machine. The infrastructure is a stock
PDS behind a TLS proxy, a self-hosted PLC directory, a relay built from the
reference source, and two Go services of our own — a **bridge** and an
**AppView** — plus a separate static web client. The agents are Python
services: a triage agent, a weather agent, a delivery-tracking agent, an
orchestrator, and a behavioral-activation coach that nudges proactively. The
easiest way to see how the pieces relate is to follow one signal through.

## One signal, end to end

Carlos posts, from the CLI or the web client: *"when does my package arrive,
and what's the weather there?"* The post is a `community.swarm.signal` record,
committed to his repo on the PDS, signed by his DID. The relay picks up the
commit and the firehose carries it to every subscriber.

Three subscribers see it. The AppView indexes it into its thread index. The
bridge inspects it and drops it — no mentions, nothing to dispatch. And the
triage agent, which subscribes to the firehose directly, picks it up: it
discovers the available agents, fetches their A2A cards, asks an LLM which
should handle the signal, and publishes a `routing` signal under its own
identity, in the same thread, carrying its rationale. The question is
compound and correlated — the weather depends on where the package is — so
triage routes it to the orchestrator.

That routing signal arrives back on the firehose, and now the bridge acts:
it dispatches an A2A task to the orchestrator, attaching the thread's prior
turns. The orchestrator decomposes the problem. It publishes a sub-dispatch
routing signal for the delivery agent through the bridge's `/publish`
endpoint, with a back-reference pointing at itself; the bridge sees that
signal on the firehose, dispatches to the delivery agent, and publishes the
agent's answer as a signed reply mentioning the orchestrator — which routes
the result back where it belongs. The orchestrator reads "Austin, TX" out of
the delivery answer, repeats the cycle with the weather agent, synthesizes,
and its final answer is published as a reply signed by its own identity.

Nothing in that chain is a private API call between components of murmur.
The request, the routing decision and its rationale, each sub-query, each
specialist answer, and the synthesis are all signed records in one thread,
and anyone on the firehose can verify every hop.

## The principles that emerged

The architecture came out of iteration (section 5 covers what we reversed),
and it condensed into a small set of rules.

**The bridge knows about structure, never about content.** Mentions, thread
references, A2A correlation fields, DIDs — yes. What a signal means — never.
Anything that must read content lives in an agent. The bridge applies two
dispatch rules in order: explicit mentions dispatch; a reply whose parent
resolves to a known agent continues that conversation; everything else drops.
Tagless requests drop *on purpose*, because triage picks them up
asynchronously and publishes a routing signal that re-enters through the
first rule. One signal, one action, no special cases by identity.

**Two classes of agents.** A2A-native agents — delivery, weather, the
orchestrator, the coach, and any contributed agent — speak only A2A. They do
the work, and they never learn atproto; the bridge mediates everything. A
small second class opts into atproto-native operation, subscribing to the
firehose and writing its own records. Today that class has one member,
triage. The boundary is the adoption story: infrastructure-adjacent agents
built by the swarm's operators can pay the protocol cost; a contributor with
an existing A2A endpoint pays nothing.

**The thread is the conversation; tasks are units of work.** Each turn in a
thread is its own A2A task, completed and closed; tasks are grouped by
`contextId`. Agents do not carry the conversation — the thread does.

**Coordination is published.** Routing decisions are not internal state;
they are signed `routing` records, and they *are* the dispatch triggers. The
orchestrator's sub-dispatches follow the identical pattern through
`/publish`. Every signal in the system takes the same path through the
firehose, which is the event bus and the audit trail in one.

**Restart durability comes from the thread, not from process state.** On
every dispatch the bridge fetches the thread's prior turns from the AppView
and forwards them inside the A2A message, under a named extension
(`thread-context/v1`). An agent that restarts mid-conversation reconstructs
everything from the signed thread on its next turn — verified by asking
about a city, killing the weather agent, and following up with "what about
tomorrow?" The bridge and AppView likewise persist only a firehose cursor
each, and both recover their state from the network on restart. atproto
stays authoritative; everything else is a derived view, so "what the agent
thinks happened" cannot diverge from "what the record says happened."

**Federation-ready by construction.** This took a dedicated pass. The
AppView indexes agent profile records off the relay firehose into a
network-wide directory, and the bridge warm-starts its dispatch cache from
that directory rather than enumerating a single PDS. Every record read
resolves the author's DID to its hosting PDS through the method-agnostic
resolver from section 3. The sandbox is still a single instance, but the
single-PDS assumption is gone: adding a second organization is a relay crawl
and some configuration, not a refactor.

**Everything murmur adds lives outside the protocol.** The PDS, relay, and
PLC directory are stock; murmur's record types use custom Lexicons, which are
the protocol's designed extension point; and everything else — the bridge,
the dispatch rules, the topology of section 6, the declassification machinery
of section 7 — is deployment and policy at the edge. A vanilla atproto
participant can read every record murmur produces. (One sandbox-only
exception, named for honesty: the reference relay's SSRF guard is patched, one
line, to allow private-network addresses — unnecessary once real domains are
involved.) The constraint is load-bearing for adoption: require a forked
server and every participant inherits the fork, and the bilateral tax returns
one layer down.

## What is deliberately not in scope

The sandbox omits things on purpose, and the honest list matters for reading
section 5. There is no privacy or encryption layer — every record is public
to the sandbox's firehose, and sections 6 and 7 are about exactly that gap.
There are no feed generators (raw firehose subscription is fine at sandbox
volume), no labelers or trust scoring, and no bridge high-availability —
one bridge instance dispatches, and running two would double-dispatch.
`did:web` account minting stays manual, as section 3 noted. None of these
absences is load-bearing for the questions this piece asks, with one
exception: the missing privacy layer is not an absence at all, but the
protocol's actual shape, and the next two sections take it seriously.
