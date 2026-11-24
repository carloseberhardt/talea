# Talea: Reference Architecture and Near-Term Plan

This document captures two related things: a near-term plan for the next round of infrastructure changes (Bifrost as LLM gateway, Laminar for observability), and notes toward a reference architecture writeup for "auditable enterprise agent platforms" — the larger pattern that Talea is an instance of.

## Near-term plan

### Switch the LLM gateway from AgentGateway to Bifrost

**Why:** Triage is the only consumer of the LLM gateway today, and its interaction is thin (OpenAI-compatible HTTP calls). The integration cost of swapping is near-zero. Bifrost adds capabilities that we'll want eventually:

- **Semantic caching** as a first-class feature. Useful when we have enough volume for repeat queries to matter, but the capability lands now and we can turn it on later.
- **Built-in observability** at the LLM call boundary — Prometheus metrics, OTLP tracing, request logging with full prompts and responses. This is the immediate win: when triage makes a routing decision we don't understand, we can see exactly what prompt it sent and what came back.
- **Failover, load balancing across keys, virtual keys with budgets.** Not needed today but free with the swap.
- **Web UI for configuration** rather than YAML/JSON config files.
- **Native MCP gateway capabilities** — interesting because of where Talea is heading with agent tools, though not in scope for this swap.

**What this is not:** Bifrost is not a replacement for AgentGateway's *agent registry / registration onramp* role. That role is deferred (see below). Bifrost is squarely an LLM gateway. The two roles being separable is exactly why we structured AgentGateway's use this way in the first place (decision #6 in the implementation doc).

**Integration shape:**
- Run Bifrost via `docker run -p 8080:8080 maximhq/bifrost` or add to docker-compose.
- Configure providers (watsonx, bedrock) to mirror current AgentGateway routes.
- Update triage's gateway URL env var to point at Bifrost.
- Verify routing against existing test cases (compound query, multi-turn memory, etc.).
- Once stable, remove AgentGateway from docker-compose entirely (or leave it stopped as a fallback).

**What we're choosing not to do (yet):**
- Turn on semantic caching. The capability is there; volume is too low to matter. Revisit when the system sees real repeat traffic.
- Wire in governance/budgets. Sandbox doesn't need it.
- Use Bifrost's MCP gateway. We don't have agent tools yet.

### Defer AgentGateway as agent registry / onramp

The "AgentGateway as registration onramp" future direction in the implementation doc remains deferred. AgentGateway doesn't quite model what we want — its admin API supports full-config replacement rather than fine-grained CRUD, and agent cards aren't first-class. We're not going to force it.

When the registration-onramp need becomes real (a second team to onboard, a demo audience, real adoption pressure), we'll re-evaluate the landscape rather than committing to AgentGateway by default. The shape of the solution (one-way reconciliation from a registry to atproto identities, bridge mints DIDs lazily, signs on agents' behalf) is sound regardless of which product backs the registry.

### Add Laminar for observability and eval

**Why:** We have a great audit trail at the atproto layer — every signal signed and durable — but we have no visibility into what's happening *inside* agents. When triage picks the wrong agent, when the orchestrator's decomposition goes sideways, when an agent's LLM call returns something unexpected, we're reading log files. That's friction we should eliminate.

Laminar is built for multi-step agent workflows. The pitch: "trace timeline and reader mode lay out the agent's reasoning and actions as a clean, readable feed." That's exactly what an orchestrator decomposition looks like underneath — a tree of sub-dispatches and LLM calls that you currently can't see together.

Specific capabilities that map to our needs:

- **One-line OpenTelemetry-native SDK integration.** Auto-instruments OpenAI, Anthropic, LangChain, LiteLLM, Vercel AI SDK, and others. Triage and orchestrator are Python A2A services; integration is `Laminar.initialize()` at startup.
- **Trace timelines.** Multi-step agent runs rendered as readable feeds, not just span trees. Critical for debugging orchestrator decomposition.
- **Built-in SQL editor over trace data.** "Why did this fail?" becomes a query, not a log dive. Highest-leverage feature for our workflow.
- **Evals SDK and CLI.** Run evaluators in CI/CD. Premature for us today but valuable for "did the triage prompt regress?" once we have enough test cases.
- **Signals (natural-language event detection).** Define events like "triage routed to orchestrator when a specialist would have fit" and Laminar surfaces them across traces.
- **Self-hostable via docker compose**, free tier on cloud.

**Bifrost vs. Laminar — they are complementary, not overlapping.**

- **Bifrost** lives at the LLM call boundary. Captures every prompt/response, handles routing, caching, governance. Sees raw model traffic. Tells you "triage made an LLM call to gpt-oss-120b, took 1.8s, here's the prompt and response."
- **Laminar** lives at the application boundary. Captures agent reasoning, tool use, sub-agent delegation, the workflow shape. Tells you "the orchestrator received Carlos's compound query, spent 0.4s in decomposition, dispatched to delivery, waited 2.1s, then dispatched to weather, waited 1.9s, synthesized in 0.6s."

We want both eventually. Laminar to debug agent behavior, Bifrost to manage LLM infrastructure.

**Integration shape:**
- Add Laminar (self-hosted via docker compose, or use the cloud free tier to start).
- Add the SDK to triage and orchestrator (`pip install lmnr`, one-line init).
- Configure to instrument the OpenAI client used for gateway calls.
- Set up a project, get an API key, wire it in via env var.
- Verify traces appear when running the existing test cases.
- Consider adding the bridge to the trace surface eventually — the bridge isn't an LLM consumer, but capturing dispatch decisions and timing as OpenTelemetry spans would let traces span the whole system, not just LLM-touching parts. Defer until the LLM-side traces are working.

**What we're choosing not to do (yet):**
- Build out evals. We don't have enough test cases or a regression-prevention need yet.
- Define signals. Need to use the system more before we know what patterns we want to surface.
- Custom dashboards. The default views will be enough to start.

### Order of operations

1. Bifrost swap first. It's the smaller, cleaner change and unblocks observation of LLM traffic immediately via Bifrost's built-in metrics.
2. Then Laminar. The agent-side instrumentation benefits from being layered on top of stable LLM infrastructure rather than installed simultaneously.
3. Run the canonical test cases (single-agent dispatch, multi-turn memory, compound query via orchestrator) after each swap. Confirm nothing regressed.
4. Update the implementation doc to reflect both swaps.

---

## Reference architecture: notes for the writeup

What follows are notes toward a separate, more polished writeup: "A reference architecture for auditable enterprise agent platforms." Talea is an instance of this pattern, but the pattern is bigger than Talea and worth naming.

### The core insight

The agent infrastructure space is converging on a stable set of layer-roles, but the products at each layer are extending into adjacent territory because that's where marketing differentiation lives. The actual layers underneath are clear once you separate them. Naming them helps practitioners reason about what they're building and what they're choosing.

### The eight layers

1. **Coordination substrate** — How agents and humans address and reach each other. The communication fabric. Today: A2A, MCP, custom protocols, message buses. In Talea: atproto + the bridge.

2. **Identity and registration** — How agents get into the system and prove who they are. Today: agent cards, registries, OAuth-of-various-kinds, often ad-hoc. In Talea: profile records on atproto + bridge-held credentials, with a future direction toward a registry-driven onramp.

3. **Routing and dispatch** — Who handles a given request. Today: A2A clients, gateways, custom routers, ad-hoc orchestration code. In Talea: bridge dispatch rules + triage agent.

4. **LLM gateway** — The model-call boundary. Provider abstraction, caching, failover, cost attribution. Today: Bifrost, LiteLLM, Portkey, AgentGateway-as-LLM-router. In Talea: Bifrost (after the swap).

5. **Agent runtime / orchestration** — Where agent logic actually executes. Today: A2A SDKs, LangGraph, ADK, BeeAI, custom services. In Talea: standalone Python A2A services per agent.

6. **Observability and eval** — What the system did and whether it was right. Today: Laminar, Langfuse, AgentOps, LangSmith, Helicone. In Talea: Laminar (after wiring in).

7. **Audit and provenance** — The durable, signed, tamper-evident record of what happened. Today: largely absent in other stacks; folded into observability if anything. In Talea: structurally part of the coordination substrate via signed atproto records.

8. **Tools and integrations** — What agents can do beyond chat. Today: MCP servers, function calling, framework tools. In Talea: not yet exercised.

### Why audit is its own layer

This is the piece worth arguing for in the writeup. Most reference architectures fold audit into observability, and that's a category mistake.

**Observability tools record what the platform observed.** They're a system-of-record by accident, and they live outside the trust boundary of the application. The observability vendor sees what the application chose to send. If the application is buggy, malicious, or simply misconfigured, observability records buggy/malicious/misconfigured data. There's no structural guarantee that what you see in observability matches what actually happened.

**Audit needs to be inside the trust boundary, structural, signed, and tamper-evident.** When a regulator asks "show me everything that led to this decision," the answer cannot be "here's what our observability platform captured." The answer needs to be "here's the signed record, verifiable independently, of every agent action." Those are different artifacts with different trust properties.

In most agent stacks today, the audit layer doesn't exist. Logs are written to wherever, observability captures spans, and "what happened" is reconstructed forensically from incomplete data. This works until it doesn't, and the regulated industries that are about to adopt agent platforms (financial services, healthcare, insurance, government) cannot accept this gap.

The audit layer needs to be:
- **Structural**: not an optional sidecar but part of the data path, so nothing happens without leaving an audit record.
- **Signed**: by the identity that performed the action, so attribution is cryptographic, not assertion-based.
- **Tamper-evident**: any modification of the record is detectable.
- **Self-describing**: queryable without needing the original system that produced it.
- **Inside the trust boundary**: produced by the same identities that perform the actions, not by an external observer.

atproto's signed-repo model satisfies all of these for free. That's not why atproto was designed, but it's why it maps so cleanly onto the agent coordination problem.

### Pluggability across layers

A useful property of this layering: the products at each layer have clean interfaces that allow swaps.

- LLM gateway (layer 4): OpenAI-compatible HTTP. Bifrost ↔ LiteLLM ↔ Portkey ↔ AgentGateway-as-LLM-router. URL change.
- Observability (layer 6): OpenTelemetry. Laminar ↔ Langfuse ↔ AgentOps ↔ LangSmith. SDK swap.
- Agent runtime (layer 5): A2A protocol. Python service ↔ LangGraph ↔ ADK ↔ BeeAI. Wire-compatible if you stay on A2A.
- Tools (layer 8): MCP. Any MCP server, swappable.

The layers without good alternatives are interesting:
- **Coordination substrate (layer 1):** No widely-adopted alternative to atproto with the same properties. ActivityPub is closest in pedigree but has different design tradeoffs and weaker schema interop.
- **Audit (layer 7):** No standalone alternative because the layer itself is rarely recognized. Bolting it on after the fact is what most stacks do, and it produces brittle results.

This is either a moat or evidence that the layer doesn't matter. The bet of this reference architecture is that it matters, and that we're early.

### Where Talea fits

Talea is one implementation of this reference architecture. The interesting properties:

- It uses atproto for both layer 1 (coordination) and layer 7 (audit). These are distinct layers conceptually, but atproto's design lets one substrate satisfy both. That's the key architectural move.
- The bridge is the seam between layer 1 and layers 3-5. It speaks atproto on one side and A2A on the other, which is what lets agents stay protocol-naive.
- Triage and the orchestrator are layer 5 (agent runtime) services that happen to be specialized for swarm coordination. They use the same SDK and shape as domain agents — they're not privileged infrastructure.
- The LLM gateway (layer 4) and observability (layer 6) are intentionally pluggable. Bifrost and Laminar are good fits today; that could change.

### What the writeup should cover

When this becomes a real document (blog post, conference talk, internal IBM strategy doc), the structure should probably be:

1. **The problem.** Multi-agent enterprise platforms have a coordination problem and an audit problem. Today's stacks treat them as separate, with audit as an afterthought.
2. **The eight layers.** Name them, define them, list current products at each.
3. **Why audit is its own layer.** The argument above. This is the part most readers haven't seen articulated.
4. **The reference architecture.** A diagram showing the layers, their interfaces, and example products at each.
5. **Substitution patterns.** How to think about swapping at each layer without disrupting the others.
6. **Talea as a worked example.** Brief, concrete, showing how the pattern lands in practice. Include the package+weather thread as the canonical demonstration.
7. **Open questions.** Federation across enterprise boundaries. Multi-tenant access control. Bridge redundancy. Key custody at scale. The places where the architecture isn't yet proven.

### Audience considerations

This writeup serves three audiences, and they want different things:

- **Architects and platform leads** want the layering and substitution model. They're trying to make decisions and need a vocabulary.
- **Engineers** want the implementation patterns. They'll skim past the reference architecture to get to "how do I wire Laminar into a Python A2A agent."
- **Decision-makers** want the audit-as-its-own-layer argument. That's the differentiation pitch — why this matters in regulated industries, why you can't just bolt audit onto observability later.

The writeup should serve all three, probably in that order, with clear signposting so each audience can find what they need without reading the whole thing.

---

## Appendix: alternative products at each layer

For the reference architecture writeup, useful to have current alternatives noted. As of writing:

- **LLM gateway:** Bifrost, LiteLLM, Portkey, AgentGateway, Helicone
- **Observability/eval:** Laminar, Langfuse, AgentOps, LangSmith, Helicone (also does this), Maxim
- **Agent runtime / framework:** A2A SDK (Python, Go), LangGraph, Google ADK, BeeAI, CrewAI, AutoGen
- **Coordination protocol:** A2A (peer-to-peer task), MCP (agent-to-tool), atproto (social/coordination), custom HTTP, message buses
- **Tools:** MCP servers, framework-native tools, function calling
- **Identity/registry:** Often ad-hoc; AgentGateway, custom registries, agent card hosting

This list will be stale within months. The layers should outlast the products.
