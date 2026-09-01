# Section 1 — Opening

> **Draft status:** first prose draft. Target ~400 words. Frames the question,
> the bilateral-tax problem, and the EDI cautionary tale, and ends on the
> honesty move — all of which section 2 assumes. Title still unsettled
> (outline Q7). EDI citation verified — see the stub at the end.

---

Cross-organization work runs on APIs, and the integration tax is brutal.
Every pair of organizations negotiates everything bilaterally: schemas,
authentication, audit obligations, error semantics. The pattern is old
enough to have a cautionary tale. EDI gave supply chains shared message
standards decades ago, and yet every trading-partner relationship still
became its own integration project — the standards arrived without a shared
substrate underneath them, so connecting to your hundredth partner cost
nearly what the first one did. A standard is not a network.

Now suppose the work shifts to agents. Not bots, not scripts — identities
with persistent state, holding multi-turn conversations, in participant sets
that change as the task demands. Several things break the API pattern at
once. The unit of work stops being a request and becomes a negotiation.
Third parties join exchanges already in flight. "What did the system do"
turns into "what did the agent decide, with what context, in front of whom."
And every one of those questions gets asked across organizational lines,
where neither side's directory, logs, or schemas are authority for the
other.

This piece is about what an agent-native substrate for that world might need
to look like. We built one — a working multi-agent system whose coordination
layer is atproto, the federated protocol underneath Bluesky, a protocol
designed for social media and not for agents at all — and ran it end to end.
What follows is the structural argument for why the substrate's properties
stop being optional (section 2), the mapping from atproto's primitives to
the problem (section 3), the system itself and what building it taught us
(sections 4 and 5), and then the part we consider the real contribution: a
multi-party design exercise (section 6) that surfaces the problem the
substrate explicitly does not solve — information flow across classification
boundaries — and why that problem is harder and more interesting than it
first appears (section 7).

One claim we are not making: that B2B work will become agent-mediated. We
do not know, and neither does anyone selling the future at the moment. The
question this piece asks is conditional — *if* the work shifts, what must
the substrate underneath provide? — and conditional questions have
structural answers, which can be checked against a running system rather
than against a forecast. That is the spirit of everything that follows.

---

### Citation stubs (format for venue later)

- C. L. Iacovou, I. Benbasat, and A. S. Dexter, "Electronic Data Interchange
  and Small Organizations: Adoption and Impact of Technology," *MIS
  Quarterly* 19(4), 1995, pp. 465–485. doi:10.2307/249629. *(Supports the
  costly-onboarding / adoption-barrier claim — organizational readiness,
  external pressure, and setup cost kept small trading partners out. The
  "hundredth partner cost nearly what the first one did" framing is ours,
  not the paper's; keep it as our gloss or soften if a reviewer balks.)*
