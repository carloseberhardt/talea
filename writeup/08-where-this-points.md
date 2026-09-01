# Section 8 — Where this points

> **Draft status:** first prose draft. Target ~700 words. Picks up section
> 7's closing handoff ("whether that trade is worth it depends on where you
> deploy it"), lays out the two targets, the open problems, and the closing
> beats.

---

Section 7 ended on a trade: maximum permanence in exchange for the audit
trail. Whether that trade is worth taking depends on where you would deploy
it, and two deployment shapes stand out — one speculative, one available
today.

**Cross-org, agent-mediated B2B** is the forward-looking case, and it
inherits section 1's honesty clause: it exists only if the work actually
shifts. What the case study adds is that the shape is structurally backed —
the properties section 2 derived are the properties the substrate ships, and
the mechanism has now run end to end, if only at sandbox scale. Identity
topology follows the section 3 matrix: `did:web` where sovereignty comes
first, a consortium PLC where a governed group wants a shared
identity-lifecycle log. Most enterprises would not deploy this today, and
should not. The question the case answers is what to reach for when the
work arrives.

**Cross-classification work inside a single organization** is the
present-day case, and it needs no forecast. Regulated functions — Legal, HR,
Finance, Internal Audit — already operate under information-flow
constraints, and today those constraints are satisfied by segregation:
separate systems, separate teams, and deliberate friction at every boundary.
That segregation is pure cost. An agent-mediated workflow that crosses those
functions while being *structurally* multi-level-secure — the section 6
topology plus the section 7 machinery — would replace some of that friction
with architecture. Identity topology here is the matrix's first row: a
self-hosted PLC, because the org is its own trust authority and the
lifecycle log is a regulatory asset rather than a liability.

The deployment shape we find most plausible sits between the two:
departmental federation. Each regulated function runs its own stack, the
public firehose is the interior of the company rather than the open
internet, functions federate selectively, and the declassification machinery
enforces the flow rules at each boundary. Everything in sections 6 and 7
applies unchanged; only the radius shrinks.

## What this writeup does not claim to solve

The open problems, plainly listed, because they are the difference between
a case study and a pitch:

- **Declassification controls.** Taint tracking and read/write separation
  are sketched in section 7, not built. This is the load-bearing gap.
- **Identity at multiple classification levels.** A DID lives on one PDS, so
  every boundary-crossing participant needs two identities, and the pairing
  between them is itself sensitive information.
- **Federation at real scale.** The code is federation-ready — the
  single-PDS assumption is gone, resolution is method-agnostic — but no
  multi-org deployment has run. Real domains, TLS, relay crawl governance,
  and the still-manual `did:web` minting flow all stand between here and
  there.
- **Bridge high availability.** One bridge instance dispatches; two would
  double-dispatch. Claim-based dedup is designed, not built.
- **Encrypted or private records.** Outside atproto's core by design, and an
  open question for any substrate of this class.

## Recap

We set out to test whether a protocol built for federated social media could
serve as the coordination substrate for multi-agent work, and the answer has
two halves. The agents-as-social-peers pattern is implementable: agents hold
identities, converse in signed threads, discover each other over the shared
stream, and protocol-naive agents participate through a bridge without
learning any of it. The two-layer split — coordination on the substrate,
execution point-to-point — held at every seam we tested. That much is
substrate-agnostic; atproto is one way to get the properties, not the only
one.

The other half is what it costs. The substrate does not solve information
flow across trust boundaries, and its best property — the permanent, signed,
replicated record — is precisely what raises the stakes of getting that flow
wrong. We learned more from that gap than from everything that worked.

This is a research artifact, not a product proposal. The code exists as
evidence; the writeup is the contribution. If the cross-org shift arrives,
or if the cross-classification bill is one your organization is already
paying, the shape sketched here is worth trying on — and the experiment we
have not run, a real deployment across real trust boundaries, is exactly
the one our own findings say matters most. That is where this points.
