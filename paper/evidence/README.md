# Reproducible design checks

These files accompany *From Conversations to Commitments: A Design Study of Shared Records for Agent-to-Business Coordination* by Carlos Eberhardt and Mansura Habiba.

## Run

Python 3.9 or later is sufficient; no third-party packages, credentials, network access or language model are required. In a copy of this directory, run:

```sh
python3 protocol_model.py
python3 boundary_checks.py
```

Each script prints its results and rewrites its adjacent JSON result file. In the repository, `python3 ../build.py checks` instead runs in a temporary directory and verifies that the results match the committed files.

## Expected results

`protocol_model.py` explores two one-unit deals. Its three reference configurations have 448, 448 and 576 reachable states and no counterexamples to the invariants actually checked. The three ablations produce counterexamples for committed-budget admission, inventory admission, and releasing a confirmed obligation's backing at timeout.

`boundary_checks.py` verifies one accepted mandate binding, rejects 13 binding mutations, and checks 18 approved work orders across 48 private-context variants each (864 equal payloads and 3,456 rejected payload mutations). It also demonstrates a schema-only value-encoding channel and a non-atomic check-then-reserve race.

## Scope

The state explorer is a finite abstraction, not an implementation of the complete gateway algorithm or AT Protocol. It uses unit exposure, atomic state transitions and trusted gateways. It does not model the full buyer-state machine, including rejection of acceptance after an observed closure, or prove the manuscript's propositions for all executions. Irreversible terminal decisions are built into its transitions. The checked invariants are inventory capacity, committed-budget capacity, the timeout-release counterexample, and execution requiring confirmation; the state counts should not be read as checks of every property in the manuscript.

The interface checks are deterministic synthetic fixtures. Their private-context variants are not inputs to the fixed-payload release function. They demonstrate the selected interface boundary, not general noninterference, cryptographic verification, timing resistance, prompt-injection resistance or a multi-organisation deployment. They do not compare coordination representations or establish an empirical benefit.

No business transaction or participant data is included.
