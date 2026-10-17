"""Finite state exploration of the paper's reservation/terminal-decision abstraction.
Python standard library only. Not an ATProto implementation or cryptographic test.
"""
from collections import deque
from dataclasses import dataclass, replace
import json
from pathlib import Path

@dataclass(frozen=True)
class State:
    accepted: tuple = (False, False)
    terminal: tuple = (0, 0)  # 0 undecided, 1 confirmed, 2 closed
    delivered: tuple = (False, False)
    executed: tuple = (False, False)
    expired: bool = False
    buyer_revoked: bool = False
    seller_revoked: bool = False

def put(t, i, value):
    return t[:i] + (value,) + t[i+1:]

def held(s, unsafe_timeout=False):
    return sum(a and not (s.delivered[i] and s.terminal[i] == 2)
               and not (unsafe_timeout and s.expired and not s.delivered[i])
               for i, a in enumerate(s.accepted))

def successors(s, budget, stock, ablation):
    for i in range(2):
        if not s.accepted[i] and not s.buyer_revoked:
            used = held(s, ablation == 'timeout_release')
            if ablation == 'no_budget_reservation' or used < budget:
                yield f'accept({i})', replace(s, accepted=put(s.accepted, i, True))
        if s.terminal[i] == 0:
            if s.accepted[i] and not s.expired and not s.seller_revoked:
                if ablation == 'no_inventory_guard' or s.terminal.count(1) < stock:
                    yield f'confirm({i})', replace(s, terminal=put(s.terminal, i, 1))
            yield f'close({i})', replace(s, terminal=put(s.terminal, i, 2))
        if s.terminal[i] and not s.delivered[i]:
            yield f'deliver({i})', replace(s, delivered=put(s.delivered, i, True))
        if s.terminal[i] == 1 and s.delivered[i] and not s.executed[i]:
            yield f'execute({i})', replace(s, executed=put(s.executed, i, True))
    for key in ('expired', 'buyer_revoked', 'seller_revoked'):
        if not getattr(s, key):
            yield key, replace(s, **{key: True})

def violations(s, budget, stock, ablation):
    bad = []
    if s.terminal.count(1) > stock:
        bad.append('inventory')
    if s.terminal.count(1) > budget:
        bad.append('committed_budget')
    if ablation == 'timeout_release':
        for i in range(2):
            if s.terminal[i] == 1 and s.expired and not s.delivered[i]:
                bad.append('commitment_without_held_funds')
                break
    if any(s.executed[i] and s.terminal[i] != 1 for i in range(2)):
        bad.append('execution_without_confirmation')
    return bad

def explore(budget, stock, ablation='none'):
    initial = State()
    paths = {initial: []}
    queue = deque([initial])
    edges = 0
    failures = {}
    while queue:
        state = queue.popleft()
        for failure in violations(state, budget, stock, ablation):
            failures.setdefault(failure, paths[state])
        for event, nxt in successors(state, budget, stock, ablation):
            edges += 1
            if nxt not in paths:
                paths[nxt] = paths[state] + [event]
                queue.append(nxt)
    return dict(budget=budget, stock=stock, ablation=ablation,
                reachable_states=len(paths), transitions=edges,
                counterexamples=failures)

def main():
    runs = [explore(b, s) for b, s in [(1, 1), (1, 2), (2, 1)]]
    runs += [explore(1, 2, 'no_budget_reservation'),
             explore(2, 1, 'no_inventory_guard'),
             explore(1, 1, 'timeout_release')]
    assert all(not r['counterexamples'] for r in runs[:3])
    assert all(r['counterexamples'] for r in runs[3:])
    result = {'scope': 'Two one-unit deals; irreversible terminal decisions; atomic transitions; no Byzantine gateway.', 'runs': runs}
    out = Path(__file__).with_name('model-results.json')
    out.write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))

if __name__ == '__main__':
    main()
