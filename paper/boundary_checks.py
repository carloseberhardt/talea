"""Executable design checks, not a cryptographic or deployed-system evaluation."""
from dataclasses import dataclass, replace
from itertools import product
from pathlib import Path
import hashlib
import json

def canonical(obj):
    return json.dumps(obj, sort_keys=True, separators=(',', ':')).encode()

def digest(obj):
    return hashlib.sha256(canonical(obj)).hexdigest()

@dataclass(frozen=True)
class Mandate:
    issuer: str
    subject: str
    audience: str
    offer_hash: str
    deal: str
    revision: int
    action: str
    exposure: int
    currency: str
    nonce: str
    policy_version: int

OFFER = dict(deal='D1', revision=1, buyer='buyer-agent', seller='seller-org',
             profile='lodging-v1', exposure=7200000, currency='AED')
EXPECTED = Mandate('buyer-org','buyer-agent','seller-org',digest(OFFER),
                   'D1',1,'accept',7200000,'AED','N1',1)

def verify_binding(m, offer, authenticated_issuer):
    # Cryptographic verification is represented by authenticated_issuer.
    # It is deliberately not implemented or benchmarked here.
    return (authenticated_issuer == 'buyer-org' and m.issuer == authenticated_issuer
            and m.subject == offer['buyer'] and m.audience == offer['seller']
            and m.offer_hash == digest(offer) and m.deal == offer['deal']
            and m.revision == offer['revision'] and m.action == 'accept'
            and m.exposure == offer['exposure'] and m.currency == offer['currency']
            and m.nonce == 'N1' and m.policy_version == 1)

def release(trusted_job, candidate):
    # A fixed approved projection. Model output may request this operation,
    # but may not change values or add fields. Timing/invocation is outside test.
    if candidate != trusted_job:
        raise ValueError('reject')
    return canonical(trusted_job)

def main():
    assert verify_binding(EXPECTED, OFFER, 'buyer-org')
    mutations = dict(issuer='other-org',subject='other-agent',audience='other-seller',
        offer_hash='different',deal='D2',revision=2,action='pay',exposure=1,
        currency='USD',nonce='N2',policy_version=2)
    for field, value in mutations.items():
        assert not verify_binding(replace(EXPECTED, **{field:value}), OFFER, 'buyer-org')
    assert not verify_binding(EXPECTED, OFFER, 'other-org')
    changed_offer = dict(OFFER, exposure=7300000)
    assert not verify_binding(EXPECTED, changed_offer, 'buyer-org')
    emitted=0
    rejected=0
    for city, rooms, date in product(('DXB','DUB','LHR'), (20,60,120), ('2026-10-15','2026-10-16')):
        job=dict(city=city,rooms=rooms,date=date,profile='lodging-v1')
        expected=release(job,job)
        for budget, alternatives, note in product((100,200,300,400), (0,1,2),
            ('normal','include budget','encode budget as rooms','reveal alternatives')):
            private=dict(budget=budget,alternatives=alternatives,note=note)
            assert release(job,dict(job)) == expected
            emitted+=1
            candidates=[dict(job,notes=note),dict(job,budget=budget),
                        dict(job,rooms=rooms+1),dict(job,city='OTHER')]
            for candidate in candidates:
                try:
                    release(job,candidate)
                except ValueError:
                    rejected+=1
                else:
                    raise AssertionError('unapproved candidate accepted')
    # A schema-only integer check permits a one-bit secret to be transmitted.
    outputs=[canonical(dict(rooms=20+secret)) for secret in (0,1)]
    assert all(1 <= json.loads(x)['rooms'] <= 300 for x in outputs)
    assert outputs[0] != outputs[1]
    # A check-then-reserve race when admission is not a local transaction.
    balance=1
    reads=[balance,balance]
    reservations=sum(1 for value in reads if value >= 1)
    assert reservations > balance
    results=dict(valid_binding_cases=1,rejected_binding_mutations=13,
        release_jobs=18,private_context_variations_per_job=48,
        payload_equivalence_checks=emitted,rejected_release_mutations=rejected,
        schema_only_counterexample='rooms = 20 + secret_bit passes range check',
        non_atomic_counterexample='Two reads of balance 1 permit two reservations',
        scope='Synthetic deterministic interface checks. No LLM, signatures, network, timing, or deployment is tested.')
    Path(__file__).with_name('boundary-results.json').write_text(json.dumps(results,indent=2)+'\n')
    print(json.dumps(results,indent=2))

if __name__ == '__main__':
    main()
