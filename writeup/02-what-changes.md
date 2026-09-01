# Section 2 — What changes when agents do the work

> **Draft status:** first prose draft of the spine section. Target ~900 words;
> assumes section 1 has already framed API-mediated B2B, the bilateral
> integration tax, and the EDI cautionary tale, and has marked the speculation
> honestly. One running example carries all four shifts.

---

Take the speculation seriously for the length of this section: suppose
cross-org work really does shift to agents. Not bots and not scripts, but
identities with persistent state, holding multi-turn conversations, in
participant sets that change as the work demands. Four things break the API
pattern at once, and it pays to look at them one at a time.

One example will carry all four. ACME's procurement agent is negotiating a
component order with a supplier's sales agent. It is a recurring order, it is
real money, and one of the parts has regulatory exposure.

**Conversations replace transactions.** The API version of a purchase is a
transaction: POST a purchase order, poll a status endpoint, done. The agent
version is a negotiation. The supplier's agent reports the part back-ordered
and counters with a near-equivalent that ships two weeks sooner; ACME's agent
checks the substitute against its tolerance requirements, accepts, and adjusts
the quantity while it's at it. Now ask where the order is. It is not in any
single request-response pair. It is in the conversation, and the conversation
has no home in a stateless protocol. Each side holds a partial log in its own
format, and when the delivered parts turn out wrong, reconstructing what was
agreed means merging two bilateral logs that were never designed to reconcile.
Anyone who has settled a dispute out of two email archives knows how that
goes.

**Participant sets become fluid.** Mid-thread, the substitution changes the
shipping classification, so ACME's agent brings in a logistics agent to
reprice freight. The substitute part contains a controlled alloy, so the
supplier's agent loops in an export-compliance agent before confirming. REST
models two parties who integrated in advance. It has no move for "a third
party joins an exchange already in flight" — today that is a new bilateral
integration and weeks of lead time, which is why it rarely happens. Agents can
discover and join in minutes, but only if there is a substrate on which to
discover and join. And the newcomer has to read the thread so far: records it
never negotiated a schema for. Bilateral schema agreements stop working the
moment participation is fluid. Schemas have to be published, machine-readable,
and resolvable by whoever shows up.

**Audit becomes load-bearing in a different shape.** When humans drive APIs,
audit answers "what did the call do," and a per-endpoint log answers it. When
agents drive, the question becomes: what did the agent decide, with what
context, talking to whom, with who else seeing what. The auditor who asks why
ACME accepted the substitution needs the supplier's counter-offer, the
tolerance check, and the compliance agent's clearance — a decision trail
spanning four agents and two organizations. Two self-maintained partial logs
do not answer it, and there is a sharper problem underneath: each log is
editable by the party it belongs to. The moment agents make decisions with
money and regulatory consequences attached, the trail itself has to be
multi-party, ordered, and hard to quietly revise. Audit stops being a log you
keep and becomes a property the infrastructure either has or lacks.

**Identity needs to be portable.** Every hop in the example crossed an
organizational boundary, and each hop begs the same question: who, exactly,
did that? Inside ACME, its agents authenticate against ACME's directory;
inside the supplier, against the supplier's. Those identities are real in
their home systems and meaningless one step outside them. An OAuth token
scoped to the supplier's identity provider proves nothing to ACME's auditor,
and less to the regulator who wants to know which agent cleared the controlled
alloy. Cross-org agentic work needs identity that means the same thing in
every system that can verify it, and that no counterparty owns. Neither side's
directory can be the root, for the same reason neither side's log can be the
audit trail.

Stack the four shifts and a requirements list falls out. A substrate no single
participant owns, because participation is fluid and crosses trust boundaries.
A signed, cross-party record of who said and decided what, because the
decisions carry consequences. Schemas that are published and machine-readable,
because the parties reading them were not at the negotiating table. Identity
that is portable and independently verifiable, because "who did that" is asked
across org lines. None of these is a new idea on its own; each exists
somewhere in the B2B stack as a product or a workaround. What the shifts
change is their status. Properties that were optional under the API pattern —
nice to have, solved bilaterally when they were solved at all — become
load-bearing under the agent pattern, and they become load-bearing together.

There is one place this exact list has already been assembled and shipped:
federated social media. A social network without a central owner faces the
same structural problem — many parties, no shared operator, fluid
participation, public accountability — and atproto's answers are portable
identity, signed repositories, published schemas, and a shared event stream.
That is not an endorsement, and it might yet be a coincidence. The rest of
this piece is the experiment that finds out.
