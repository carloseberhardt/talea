"""
Triage agent — given a signal, decide which agents in the swarm should handle it.

The first "thinking" agent in murmur. The bridge calls this over A2A when a
signal arrives without explicit @-mentions and without an obvious chat-
continuation parent. Triage:

  1. Discovers the available agents from the bridge's HTTP /agents endpoint
     (the bridge is the only thing in the system that touches atproto for
     discovery — triage stays atproto-naive).
  2. Fetches each agent's A2A card directly from the agent's endpoint to
     get rich skill descriptions. The agent card is the source of truth.
  3. Asks an LLM (via AgentGateway, defaulting to gpt-oss-120b on watsonx)
     which agents should handle the signal.
  4. Returns a JSON object {"mentions": [...], "rationale": "..."} that the
     bridge will turn into a routing reply in the thread.

Run: uv run python triage_agent.py
Serves on port 5050 by default (override with TRIAGE_PORT).
"""

import asyncio
import json
import logging
import os
import re
from typing import Any

import httpx
import uvicorn
from a2a.server.agent_execution import AgentExecutor, RequestContext
from a2a.server.apps import A2AStarletteApplication
from a2a.server.events import EventQueue
from a2a.server.request_handlers import DefaultRequestHandler
from a2a.server.tasks import InMemoryTaskStore
from a2a.types import AgentCapabilities, AgentCard, AgentInterface, AgentSkill
from a2a.utils.message import new_agent_text_message
from openai import AsyncOpenAI

# ── Configuration ───────────────────────────────────────────────────

PORT = int(os.getenv("TRIAGE_PORT", "5050"))
BRIDGE_DISCOVERY_URL = os.getenv("BRIDGE_DISCOVERY_URL", "http://localhost:2471")
LLM_BASE_URL = os.getenv("LLM_BASE_URL", "http://localhost:4000/watsonx")
LLM_MODEL = os.getenv("LLM_MODEL", "openai/gpt-oss-120b")

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(name)s] %(levelname)s: %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("triage")

# OpenAI-compatible client pointed at AgentGateway. The "api_key" is required
# by the SDK constructor but the gateway doesn't actually check it in this
# sandbox config — leave as a placeholder.
llm_client = AsyncOpenAI(base_url=LLM_BASE_URL, api_key="not-needed")


# ── Discovery and card fetching ─────────────────────────────────────


async def discover_agents(http: httpx.AsyncClient) -> list[dict[str, Any]]:
    """Get the list of registered agents from the bridge."""
    try:
        resp = await http.get(f"{BRIDGE_DISCOVERY_URL}/agents", timeout=5.0)
        resp.raise_for_status()
        return resp.json()
    except Exception as e:
        log.warning("agent discovery failed: %s", e)
        return []


async def fetch_agent_card(http: httpx.AsyncClient, endpoint: str) -> dict[str, Any] | None:
    """Fetch and return an agent's A2A card, or None on failure."""
    url = endpoint.rstrip("/") + "/.well-known/agent-card.json"
    try:
        resp = await http.get(url, timeout=5.0)
        resp.raise_for_status()
        return resp.json()
    except Exception as e:
        log.warning("failed to fetch agent card at %s: %s", url, e)
        return None


def format_agents_for_prompt(agents: list[dict[str, Any]], cards: dict[str, dict[str, Any]]) -> str:
    """Render the available-agents section of the LLM prompt."""
    lines = []
    for i, agent in enumerate(agents, start=1):
        handle = agent.get("handle", "")
        if not handle:
            continue
        card = cards.get(handle)

        name = (card or {}).get("name") or agent.get("name") or handle
        desc = (card or {}).get("description", "")

        lines.append(f"{i}. @{handle} ({name})")
        if desc:
            lines.append(f"   {desc}")

        skills = (card or {}).get("skills") or []
        if skills:
            lines.append("   Skills:")
            for s in skills:
                sid = s.get("id", "")
                sname = s.get("name", "")
                sdesc = s.get("description", "")
                lines.append(f"     - {sid}: {sname} — {sdesc}")
        lines.append("")
    return "\n".join(lines).strip()


# ── LLM prompt and call ─────────────────────────────────────────────

SYSTEM_PROMPT = """You are the triage agent for a multi-agent swarm. Your job is to read a user's
message and decide which agent(s) in the swarm should handle it.

You will be given:
  - The user's message
  - A list of available agents, each with a description and skills

Decide which agents are best suited to handle the message. Return your
decision as a JSON object in EXACTLY this format:

{
  "mentions": ["handle1.test", "handle2.test"],
  "rationale": "Brief one-sentence explanation of your choice."
}

Rules:
  - Use atproto handles (e.g., "agent-one.test"), not display names.
  - If a specialist agent is a good fit, route to that agent.
  - If multiple specialist agents fit, include them all.
  - If NO specialist agent is a good fit, route to the general-purpose agent
    (it will be in the list — look for the one that handles general questions).
    Never return an empty mentions list. There is always at least one agent
    that can help.
  - Respond with ONLY the JSON object. No prose, no markdown fences, nothing else."""


def build_user_prompt(user_message: str, agents_section: str) -> str:
    return f"""User's message:
"{user_message}"

Available agents:

{agents_section}

Decide which agents should handle this message. Respond with the JSON object only."""


async def call_llm(user_message: str, agents_section: str) -> dict[str, Any]:
    """Call the LLM via AgentGateway and return the parsed routing decision."""
    user_prompt = build_user_prompt(user_message, agents_section)

    log.info("calling LLM (model=%s)", LLM_MODEL)
    completion = await llm_client.chat.completions.create(
        model=LLM_MODEL,
        messages=[
            {"role": "system", "content": SYSTEM_PROMPT},
            {"role": "user", "content": user_prompt},
        ],
        temperature=0.1,
    )

    raw = (completion.choices[0].message.content or "").strip()
    log.info("LLM raw response: %s", raw)
    return parse_routing_decision(raw)


def parse_routing_decision(raw: str) -> dict[str, Any]:
    """Pull the JSON object out of the LLM response. Tolerant of stray prose
    or code fences — finds the first {...} block and parses it. Returns an
    empty-mentions decision on any failure."""
    if not raw:
        return {"mentions": [], "rationale": "LLM returned empty response."}

    # Strip common fence patterns first
    fenced = re.sub(r"^```(?:json)?\s*", "", raw)
    fenced = re.sub(r"\s*```$", "", fenced)

    # Try direct parse
    for candidate in (fenced.strip(), raw.strip()):
        try:
            obj = json.loads(candidate)
            return _normalize_decision(obj)
        except json.JSONDecodeError:
            pass

    # Last resort: extract the first {...} block
    match = re.search(r"\{.*\}", raw, re.DOTALL)
    if match:
        try:
            obj = json.loads(match.group(0))
            return _normalize_decision(obj)
        except json.JSONDecodeError:
            pass

    log.warning("could not parse LLM response as JSON")
    return {"mentions": [], "rationale": f"Could not parse routing response: {raw[:200]}"}


def _normalize_decision(obj: Any) -> dict[str, Any]:
    """Coerce arbitrary parsed-JSON into a {mentions, rationale} dict."""
    if not isinstance(obj, dict):
        return {"mentions": [], "rationale": "LLM returned non-object response."}

    mentions_raw = obj.get("mentions") or []
    if not isinstance(mentions_raw, list):
        mentions_raw = []
    mentions = [str(m).lstrip("@") for m in mentions_raw if isinstance(m, (str, int))]

    rationale = obj.get("rationale") or ""
    if not isinstance(rationale, str):
        rationale = str(rationale)

    return {"mentions": mentions, "rationale": rationale}


# ── A2A executor ────────────────────────────────────────────────────


class TriageExecutor(AgentExecutor):
    async def execute(self, context: RequestContext, event_queue: EventQueue) -> None:
        user_input = (context.get_user_input() or "").strip()
        log.info("◀ received: %s", user_input[:200])

        if not user_input:
            decision = {"mentions": [], "rationale": "Empty input."}
            await event_queue.enqueue_event(new_agent_text_message(json.dumps(decision)))
            return

        async with httpx.AsyncClient() as http:
            agents = await discover_agents(http)
            log.info("discovered %d agent(s)", len(agents))

            if not agents:
                decision = {"mentions": [], "rationale": "No agents currently registered in the swarm."}
                await event_queue.enqueue_event(new_agent_text_message(json.dumps(decision)))
                return

            # Fetch all agent cards in parallel
            card_results = await asyncio.gather(
                *(fetch_agent_card(http, a["a2aEndpoint"]) for a in agents),
                return_exceptions=False,
            )
            cards = {a["handle"]: c for a, c in zip(agents, card_results) if c}
            log.info("fetched %d agent card(s)", len(cards))

        agents_section = format_agents_for_prompt(agents, cards)
        try:
            decision = await call_llm(user_input, agents_section)
        except Exception as e:
            log.error("LLM call failed: %s", e)
            decision = {"mentions": [], "rationale": f"LLM call failed: {e}"}

        log.info("▶ decision: %s", decision)
        await event_queue.enqueue_event(new_agent_text_message(json.dumps(decision)))

    async def cancel(self, context: RequestContext, event_queue: EventQueue) -> None:
        pass


# ── Agent card and app setup ────────────────────────────────────────


def build_app():
    agent_card = AgentCard(
        name="triage_agent",
        description=(
            "Routes swarm signals to the right agents. Given a user's message and the set of "
            "available agents, decides which agents (if any) should handle it. Returns a JSON "
            "object with the recommended @-mentions and a brief rationale."
        ),
        supported_interfaces=[
            AgentInterface(url=f"http://localhost:{PORT}", protocol_binding="JSONRPC"),
        ],
        version="0.1.0",
        default_input_modes=["text/plain"],
        default_output_modes=["text/plain"],
        capabilities=AgentCapabilities(streaming=False),
        skills=[
            AgentSkill(
                id="swarm_triage",
                name="Swarm Triage",
                description=(
                    "Read a signal from the swarm and decide which agents should handle it. "
                    "Discovers available agents from the bridge, fetches their cards, and asks "
                    "an LLM to make a routing decision. Returns mentions + rationale as JSON."
                ),
                tags=["swarm", "routing", "triage"],
                examples=[
                    "where is package 241234?",
                    "remind me to call mom",
                    "what's the weather like today?",
                ],
            ),
        ],
    )

    executor = TriageExecutor()
    handler = DefaultRequestHandler(
        agent_executor=executor,
        task_store=InMemoryTaskStore(),
    )

    server = A2AStarletteApplication(
        agent_card=agent_card,
        http_handler=handler,
        enable_v0_3_compat=True,
    )
    return server.build()


app = build_app()


if __name__ == "__main__":
    log.info("triage agent starting on port %d", PORT)
    log.info("  bridge discovery: %s", BRIDGE_DISCOVERY_URL)
    log.info("  LLM base URL:    %s", LLM_BASE_URL)
    log.info("  LLM model:       %s", LLM_MODEL)
    uvicorn.run(app, host="0.0.0.0", port=PORT)
