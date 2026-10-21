# Merged paper: section plan

Working title: *From Conversations to Commitments* (keep v4's). Target ~7,500 body words; v4 is ~9,000.

The shape: the bus is the built contribution, H1 says what it's for, commerce is the application that stresses it, the three-hotel case is where it bends. Each row says where the material comes from so nothing is written twice.

| # | Section | Words | Source | What changes |
|---|---|---|---|---|
| 1 | Introduction | 500 | v4 §1, `writeup/01` | Lead with the bus and what it carries (identity, non-repudiation, schemas, discovery). State the claim and the limit in the first page. |
| 2 | Background | 900 | v4 §2.1, Table 1; `writeup/03` | 2.1 A2B requirements C1–C6 and Table 1 stay. 2.2 **Why atproto** replaces v4's "The AT Protocol": primitives through an agent lens, the requirement→primitive mapping (merge with Table 2), ends on what it does not give you (privacy is topology, no unsend). ~500 of the 900. |
| 3 | Hypothesis | 500 | v4 §3 | H1 verbatim. Add the boundary as an explicit variable: participants inside one trust boundary vs. across competing counterparties. Drop the safety-props paragraph (moves to §5). |
| 4 | Murmur: the coordination bus | 1,500 | v4 §4.1–4.2, `writeup/04`, `writeup/03` two-layers | The built thing, described as the contribution. Record families and Lexicons; DID→key, signed commits, strong refs; triage, bridge, AppView; thread-context extension; why execution stays on A2A and coordination on atproto. Package-and-weather trace and restart exercise stated as demonstrations. Identity topology (PLC vs did:web) in one paragraph. Keep Fig 2, Fig 3, Table 3. |
| 5 | Commercial commitments on the bus | 900 | v4 §4.3–4.6 | Gateway, mandates, reserve/allocate/one terminal slot, durable outbox, release interface (Eq. 5). Props 1–3 become one paragraph citing Gray–Lamport; Algorithm 1 and proofs go to an appendix. |
| 6 | Worked case: disruption accommodation | 1,400 | v4 §5, v3 §7.2 | 6.1 single hotel, 120 rooms, ~400 words, keep Fig 5. 6.2 **three hotels**, ~1,000 words: the record graph, where the cross-deal correlation has to go private, the counter-offer as a value channel (v3's `rooms = 20 + s`), what each participant can read. One new figure: the three-hotel record graph with visibility regions. |
| 7 | The boundary | 700 | `writeup/06`, `writeup/07`, note of 10-19 | The finding. Reconstruction coverage per participant class (hub insider, spoke, late joiner each side). Across competing counterparties the shared record collapses to pairwise plus the hub's private union; the comparator matches it for every spoke. The benefit lands inside the boundary. H1 restated with that boundary. |
| 8 | Evidence and research agenda | 700 | v4 §6 | Finite model and fixtures to one paragraph each, keeping the two counterexamples (`rooms = 20 + s`, check-then-reserve race), dropping the 864/3,456 counts. The comparison experiment gains the multi-supplier workload and per-class coverage as the measurement. |
| 9 | Discussion and conclusion | 400 | v4 §7–8 | Shorter. The strongest competing explanation stays. |

Figures: keep 1, 2, 3, 5; add the three-hotel graph; Figs 4 and 6 move to the appendix with Algorithm 1. Tables: keep 1 and 3; merge 2 with the `writeup/03` mapping.

## What moves for each of us

Mansura's H1, §2.1, Table 1, the gateway rules, the release interface, the finite model and the single-hotel booking all stay; the proofs and algorithm go to an appendix and the counts shrink. Carlos's bus description, why-atproto, and the multi-party/declassification material come in as §4, §2.2 and §7, cut to the budgets above.

## For the call

1. Does Mansura agree the propositions and algorithm belong in an appendix, or does she see them as a contribution in their own right?
2. Is adaptive multi-supplier negotiation in scope? The three-hotel case needs a yes.
3. Author order and the AI-assistance statement from v3.
4. Venue, which fixes the length and whether an appendix counts.
