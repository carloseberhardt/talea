# did:web Spike — Findings

**Date:** 2026-06-18
**Status:** Resolution side **done & proven**. Provisioning side **investigated, deferred** (manual, no turnkey path on stock PDS). Demo stays `did:plc`.

## Why this spike

The federation-ready code pass (AppView-as-directory, bridge-consumes-directory, DID→PDS resolver) made the stack method-agnostic about identity. `did:web` is the sovereignty-first identity option for cross-org federation (see the identity-topology matrix in `writeup-outline.md §3`). Before relying on that story we needed to check two things:

- **(a)** Can the stack actually *mint* a `did:web` identity?
- **(b)** Does our code actually *resolve* `did:web` identities to their PDS?

## Finding (b): Resolution — DONE

Our `Resolver` (indigo `identity.Directory`, in `appview/cmd/appview/resolver.go` and `bridge/cmd/bridge/resolver.go`) resolves `did:web` for real: HTTPS `GET https://<domain>/.well-known/did.json` → parse DID doc → `#atproto_pds` service endpoint, with the local PDS as fallback.

Proven by a unit test that stands up a TLS server, serves a hand-crafted `did.json`, and runs it through the actual indigo `did:web` path (indigo forces HTTPS, so the test redirects the dial and trusts the test cert). **The consumption side is method-agnostic regardless of how identities are minted.**

> Note: that regression test (`TestResolverResolvesDIDWeb`) was written during the spike and is **not currently in `main`** (chunk 3 merged without it). Worth re-adding to `bridge/cmd/bridge/resolver_test.go` if we want it tracked. The mock-directory `TestResolverPDSForDID` (resolve + fallback behavior) is in `main`.

## Finding (a): Provisioning — manual, no turnkey path on `pds:0.4`

The stock Bluesky PDS is `did:plc`-centric. From the `createAccount` source + atproto docs:

- `createAccount` accepts a caller-supplied `did`, **but only with a pre-reserved keypair** (`reserveSigningKey`). For such DIDs it sets `plcOp = null`, marks the account `deactivated`, and **does not host the DID document**.
- There is **no onramp** that mints a `did:web` and hosts its doc for you. (Upstream issue, still open in spirit: *"did:web accounts seem to be impossible to create right now."*)

So `did:web` is provisionable but is a manual, multi-step flow.

### Manual minting recipe (for the future walkthrough — NOT done yet)

Single machine, using the existing PDS + caddy + relay:

1. **Reserve a signing key:** `com.atproto.server.reserveSigningKey` (public, no auth) for the target DID → PDS returns the public key it has reserved (and holds the private key).
2. **Host the DID doc** at `https://<domain>/.well-known/did.json` (via caddy), containing:
   - `id`: `did:web:<domain>`
   - `verificationMethod`: `#atproto` → the reserved signing key (Multikey)
   - `service`: `#atproto_pds` → `https://pds.talea.internal`
   - `alsoKnownAs`: `at://<handle>`
3. **Create the account** with the existing DID: `com.atproto.server.createAccount` supplying `did: did:web:<domain>` + handle + password + an **offline service-auth token** proving control. Practical tooling: `goat account create` (see bnewbold's guide; `goat account service-auth-offline` for the token).
4. **Activate** the account (it lands `deactivated`): `com.atproto.server.activateAccount`.

### Infra still to verify in a live walkthrough

- Confirm the above against the actual `pds:0.4` image (findings are read from current source + docs).
- **Relay** must resolve `did:web` to validate commits — confirm it does for sandbox domains given `--allow-insecure-hosts` + the private CA.
- **TLS:** `.well-known/did.json` must be served over HTTPS the relay/resolver trusts (caddy + CA bundle).
- **Handle resolution** for `did:web` identities still needs DNS TXT `_atproto.<handle>` or `https://<handle>/.well-known/atproto-did`. The sandbox's `.test` handles aren't resolvable — fine for our internal use (we read handles from the PDS via `describeRepo` and use `SkipHandleVerification`), but true cross-org interop wants real, resolvable handles.

## Decision

- **Demo stays `did:plc`** — it works today, and our code already follows DIDs to their PDS, so nothing about the demo changes.
- **`did:web` is the documented federation option**: resolution is free; minting is the manual `reserveSigningKey` → host-doc → `goat createAccount` → activate flow above. Bounded infra work for actual-federation time, not a refactor.
- **No blocker found.** The one-liner for the writeup's identity section: *"resolution is free; minting is the manual part."*

## TODO (later, not now)

- Walk through the manual `did:web` minting flow end-to-end on the live sandbox (validates step (a) against `pds:0.4` and the relay's `did:web` resolution).
- Re-add `TestResolverResolvesDIDWeb` if we want the `did:web` resolution path covered in CI.

## Sources

- reserveSigningKey: https://docs.bsky.app/docs/api/com-atproto-server-reserve-signing-key
- did:web creation issue #2100: https://github.com/bluesky-social/atproto/issues/2100
- Account Migration guide: https://atproto.com/guides/account-migration
- Creating a did:web atproto account using goat (bnewbold): https://whtwnd.com/bnewbold.net/3mdc7fpbxhk26
