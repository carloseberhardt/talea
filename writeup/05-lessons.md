# Section 5 — What the implementation taught us

> **Draft status:** first prose draft. Target ~1,100 words. Compresses the
> "What We've Proven" and "Infrastructure Decisions Made" material from
> `IMPLEMENTATION.md`, organized as: what worked, the meta-finding, what we
> reversed.

---

A case study earns its keep by reporting what surprised its authors, so this
section leads with the honest inventory: what worked as designed, the finding
that reframed the whole experiment, and the four places we built the wrong
thing first.

## What worked as designed

**The two-layer model holds.** The delivery agent — a plain A2A service that
has never heard of atproto — participates fully in the swarm: receives tasks,
returns results, appears in signed threads under its own identity. The claim
that most agents can stay protocol-naive survived contact with running code.

**Multi-turn conversation works across the seam.** Ask about weather in
Austin, follow up without naming the city, and the agent answers about
Austin — with each turn a separate A2A task grouped by `contextId`, and the
history reconstructed from the signed thread rather than from anything the
agent remembered. Combined with firehose cursors, this made the durability
story composable: a request that lands while the bridge is down still gets
routed, dispatched, and answered after restart, because each component
recovers its own role from the network and the swarm-wide invariant emerges
from those local recoveries. There is no global coordinator to fail.

**The thread is genuinely tamper-evident audit.** "Who decided what, when,
talking to whom" is not a log anyone maintains; it is the data structure the
coordination already happens in. Triage's routing rationale, the
orchestrator's decomposition, every specialist answer — signed, ordered,
replayable.

**Discovery needs no central registry.** Agents advertise themselves as
profile records; the AppView indexes them off the firehose into a directory
that is network-wide by construction. A new subscriber learns the whole
swarm's roster by reading the stream.

## The meta-finding

The properties that make atproto compelling only become load-bearing at
multi-party scale — and the sandbox has one party.

As a single operator we paid the full protocol cost: five services, custom
Lexicons, TLS plumbing, identity infrastructure, a relay built from source.
And we collected almost none of the structural benefit. Signed portable
identity matters when a counterparty needs to verify it; we were our own
counterparty. Tamper-evident cross-party audit matters when parties might
distrust each other's logs; we trusted ourselves fine. Published schemas
matter when someone arrives who never negotiated them; nobody arrived. For
one user, a database and a message queue would have been strictly less work.

So the experiment is most informative about what it cannot test, which is
exactly the multi-party case the substrate exists to serve. We do not read
this as a failure of the experiment, but it is a real result: this class of
substrate has a minimum viable scale larger than one, and any honest
evaluation of it has to either deploy across real trust boundaries or reason
carefully about what would happen there. We could not do the first with a
sandbox, so section 6 does the second on paper.

## What we built wrong first

Four reversals, each of which taught us where a responsibility actually
belonged.

**Triage began as an ordinary A2A agent, and the bridge kept sprouting
special cases for it.** A dedicated environment variable, a synchronous
routing call, a fallback path when triage returned nothing — all triage-only.
The fix was to invert it: triage now subscribes to the firehose directly and
publishes routing signals under its own identity, and the bridge became
uniform, dispatching on any mention-bearing signal regardless of author. The
principle came out sharper than we had it going in: agents that *are*
infrastructure may opt into the protocol; agents that do domain work never
have to.

**The bridge briefly drove the orchestrator's loop.** When compound queries
arrived, we had the bridge parse dispatch instructions, sub-dispatch to
specialists, and feed results back — which made the bridge stateful and gave
it two jobs. We pulled the loop back into the orchestrator, which now
publishes its own sub-dispatch signals through `/publish` and drives its own
cycle. The bridge went back to being event-driven and stateless per signal.
The dividing line we ended with: the bridge adapts protocols; the
orchestrator is the thing that thinks.

**A gateway sat on the dispatch path, and made everything worse.** Routing
A2A dispatch through an API gateway created a second registry — agent
profiles in atproto *and* routes in the gateway — with the bridge responsible
for keeping them synchronized, and made the bridge a fatter single point of
failure. We removed it. It later came back in a different role, as an LLM
router for the agents' own model calls, where it conflicts with nothing. The
lesson was about roles rather than products: the same component can be wrong
on one path and right on another.

**Discovery was wired backwards.** Originally the AppView proxied the
bridge's agent registry, and the bridge populated that registry by
enumerating a single PDS. Both dependencies were inverted. A per-bridge
registry can only ever know what one bridge learned, and enumerating one PDS
bakes in a single-host assumption; a relay-fed indexer sees the whole
network by construction. The AppView now owns the directory and the bridge
warm-starts from it. Discovery turned out to be core indexer work that had
been misfiled as a dispatch concern.

The pattern across all four: each reversal moved a responsibility toward
the component structurally suited to it — uniformity into the bridge,
thinking into agents, indexing into the AppView — and each had announced
itself as accumulating friction well before we named it. None of them
required abandoning the design. That is the good news half of the report.

The half the sandbox cannot deliver is what happens when there is more than
one of us. The next section takes the multi-party case seriously, on paper,
and follows a single query across three independently operated stacks and a
privacy boundary.
