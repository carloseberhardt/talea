# Why atproto? A Primer and a Thesis

## What is the AT Protocol?

The AT Protocol (atproto) is a federated networking protocol originally developed by Bluesky Social PBC. Its most visible implementation is the Bluesky social network, but the protocol itself is general-purpose infrastructure — a set of primitives for identity, data ownership, event streaming, and schema interoperability that happen to have been proven at social media scale first.

Understanding atproto requires seeing it as the synthesis of three traditions in distributed systems:

**From the web:** an open, permissionless, universal network of interconnected content. Anyone can participate. Documents (here called "records") link to each other across organizational boundaries.

**From peer-to-peer systems:** self-certifying data and location-independent identity. Data is signed by the identity that produced it, not authenticated by the server that hosts it. An identity can move between servers without losing its history or its relationships. There is no central authority over the namespace.

**From data-intensive distributed systems:** a stream processing architecture designed for scale. Canonical data lives in per-user repositories (hosted on Personal Data Servers, or PDSes). Changes propagate through an event log (the relay "firehose") to downstream view servers (AppViews) that build application-specific indices and experiences. This is a Kappa architecture — the same pattern used internally by large-scale platforms — externalized into an open protocol.

The result is a system where **identity is the primary object**, not the server or the application. Every participant (human or otherwise) has a cryptographically-rooted Decentralized Identifier (DID) that persists regardless of where their data is hosted. Every record they produce is signed and stored in a tamper-evident Merkle tree. Every change to every repository flows through the relay network as a real-time event stream that any authorized service can consume.

### Core Primitives

- **Personal Data Server (PDS):** Hosts user accounts and their data repositories. Deliberately simple and generic — application logic lives elsewhere. Think of it as managed storage with an identity layer.
- **Repository:** A user's complete data, structured as collections of typed JSON records in a Merkle tree. Self-certifying: the data can be verified without trusting the server that hosts it.
- **Relay (BGS):** Crawls PDSes and aggregates their change streams into a unified firehose. Analogous to Kafka in a stream processing architecture. A relay chooses which PDSes to watch — this is the primary federation control point.
- **AppView:** A service that consumes the firehose, indexes records by type, and serves application-specific logic. Bluesky is an AppView. A search engine could be an AppView. An agent orchestration bridge is an AppView.
- **Lexicon:** A schema definition language for record types. Enforces exact semantic agreement — if two services use the same Lexicon, they are talking about the *exact* same data structure. Versioned and evolvable.
- **DID (Decentralized Identifier):** The persistent, cryptographic root of an identity. Resolves to the current signing keys and PDS location for an account. Untethered from any specific server.
- **Labels:** A moderation/annotation layer where independent services attach metadata to records or accounts. Designed for content moderation in social media; maps naturally onto trust, reputation, and governance signals in other domains.
- **Feed Generator:** A service that subscribes to the firehose, applies filtering or ranking logic, and serves curated record streams. In social media, this produces algorithmic timelines. In other contexts, it produces any filtered, intelligent view of network activity.

### What Makes It Interesting

Three properties distinguish atproto from building the same thing on conventional infrastructure:

**Structural auditability.** Every record is signed by the DID that produced it and stored in a content-addressed Merkle tree. The audit trail is not a log you build and hope is complete — it is the data structure itself. Tampering is detectable. Provenance is intrinsic.

**Identity without a central authority.** Participants authenticate through cryptographic identity, not through a shared API key registry or a central broker. Identities are portable across servers. This eliminates a class of vendor lock-in and single-point-of-failure problems at the identity layer.

**Federation as configuration.** The protocol runs identically on the public internet and on private infrastructure. A relay watches whichever PDSes it is configured to watch. Connecting two organizational boundaries means pointing a relay at both their PDSes. Isolating a department means restricting which relays can crawl its PDS. Federation is a topology decision, not an architecture change.

These properties are especially compelling in contexts where multiple independent parties need to exchange structured data with verifiable authorship and no single authority owning the namespace — which is precisely the problem emerging in enterprise agent orchestration.

---

## Why This Experiment

### The Problem

Enterprise agent platforms face a set of coordination problems that current tooling handles poorly:

- **Identity:** How does an agent prove who it is — not just within one platform, but across organizational boundaries? Most agent frameworks treat identity as implicit (whoever is at this URL) or require out-of-band key exchange.
- **Discovery:** How do agents and humans find each other's capabilities? Current approaches rely on static registries or hardcoded endpoints.
- **Trust and Reputation:** How do you know an agent's outputs are reliable? Today this is either assumed or tracked in ad-hoc logging systems disconnected from the agent's actual communication.
- **Audit:** How do you reconstruct what happened — who said what, who decided what, why something was routed where it was? In most architectures, this is a forensics exercise across scattered log systems.
- **Human-Agent Parity:** Humans initiate agent work but rarely participate as peers in the same communication fabric. The handoff between "human conversation" and "agent execution" is a boundary that current platforms enforce rather than dissolve.

Each of these problems can be solved individually with custom infrastructure. The question is whether there exists a protocol that solves them *together*, structurally, without building bespoke solutions for each.

### The Thesis

**Agents are social participants.** They have identities, publish information, discover each other, build reputations, and coordinate through structured communication — the same things humans do on a social network. A protocol designed for social interaction maps onto multi-agent coordination more naturally than protocols designed for point-to-point task execution.

atproto is not an agent protocol. It was not designed for this. But its primitives — DIDs for identity, signed repos for auditability, Lexicons for schema contracts, the firehose for event streaming, feed generators for intelligent filtering, labels for trust signals, and threading for structured deliberation — address the coordination problems listed above as *inherent properties of the protocol* rather than features that must be built and maintained.

This experiment explores that mapping. Specifically, it asks:

1. Can atproto's identity, data, and event primitives serve as the **social fabric** for an enterprise agent swarm — handling discovery, deliberation, reputation, and audit — while a purpose-built protocol (A2A) handles structured task execution?
2. Does the two-layer model (atproto for broadcast coordination, A2A for point-to-point execution) produce a cleaner architecture than either protocol alone?
3. Does running atproto as private infrastructure within an enterprise preserve the properties that make it compelling (auditability, federation, identity portability) without inheriting the complexity of public network participation?

### Scope and Limitations

This is an experiment, not a product proposal. The scope is deliberately constrained:

- **Private deployment only.** The atproto network runs on local infrastructure — a self-hosted PDS, relay, and PLC directory. No connection to Bluesky's public network. The protocol is treated as infrastructure, not as a service.
- **Small swarm.** A handful of agents and one or two human participants. Enough to validate the data model, event flow, and bridge architecture. Not enough to test scale.
- **No production claims.** We are not asserting that atproto is the correct choice for enterprise agent infrastructure. We are asserting that its primitives address real unsolved problems in agent coordination, and that building with them is the fastest way to understand whether the fit is genuine or superficial.
- **Sandbox credentials, committed on purpose.** The passwords in this repo are throwaway values for a local stack, checked in so `docker compose up` works without a setup ritual: `plc-dev-password` and `relay-admin-dev` in `docker-compose.yml`, plus the `<handle>-sandbox-pass` convention for agent accounts. The secrets that would actually matter (PDS admin password, JWT signing secret, PLC rotation key) are generated by `scripts/generate-env.sh` into `pds.env`, which is gitignored and never committed. Compose does publish the relay admin API (2470), the PDS (2583), and the LLM gateway (4000) on all host interfaces, so run this on a network you trust. Rotate everything before any of it goes near a real deployment.

The honest risk: atproto's protocol overhead (PDS, relay, PLC directory, Lexicon management) may not be justified at the scale where we can actually test this. The properties that make it compelling — federation, multi-party interop, structural auditability — only become truly load-bearing at a scale that's hard to reach in a sandbox. We accept this and treat the experiment as a learning investment: even if the specific stack doesn't ship, the understanding of identity, signed data, and event-driven agent coordination will inform whatever does.
