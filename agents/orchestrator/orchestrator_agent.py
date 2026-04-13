"""
Orchestrator agent — decomposes compound queries across specialist agents.

The orchestrator in the murmur swarm. Triage routes here when a query needs
multiple specialists with correlated sub-queries, or when no specialist fits.

For compound queries: discovers available agents, uses an LLM to decide which
agent to ask next, and publishes a signal via the bridge's POST /publish
endpoint. The bridge dispatches to the specialist and forwards the result
back to the orchestrator (same contextId). This repeats until the
orchestrator has enough information to synthesize a final answer.

For simple queries that don't need specialists: answers directly via LLM.

Each execute call is stateless — the orchestrator reconstructs conversation
history from its task store. The multi-turn loop is driven by the bridge
forwarding specialist results back, each triggering a new execute call.

Run: uv run python orchestrator_agent.py
Serves on port 5052 by default (override with ORCHESTRATOR_PORT).
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
from a2a.server.tasks.task_updater import TaskUpdater
from a2a.types import AgentCapabilities, AgentCard, AgentInterface, AgentSkill
from a2a.utils.message import new_agent_text_message
from openai import AsyncOpenAI

# ── Configuration ──────────────────────────────────────────────────

PORT = int(os.getenv("ORCHESTRATOR_PORT", "5052"))
LLM_BASE_URL = os.getenv("LLM_BASE_URL", "http://localhost:4000/watsonx")
LLM_MODEL = os.getenv("LLM_MODEL", "openai/gpt-oss-120b")
BRIDGE_DISCOVERY_URL = os.getenv("BRIDGE_DISCOVERY_URL", "http://localhost:2471")
MAX_ROUNDS = 10

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(name)s] %(levelname)s: %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("orchestrator")

llm_client = AsyncOpenAI(base_url=LLM_BASE_URL, api_key="not-needed")

# ── Agent discovery ────────────────────────────────────────────────

SKIP_HANDLES = {"triage-one.test", "orchestrator.test"}


async def discover_agents(http: httpx.AsyncClient) -> list[dict[str, Any]]:
    """Get the list of registered agents from the bridge discovery endpoint."""
    try:
        resp = await http.get(f"{BRIDGE_DISCOVERY_URL}/agents", timeout=5.0)
        resp.raise_for_status()
        return resp.json()
    except Exception as e:
        log.warning("agent discovery failed: %s", e)
        return []


async def fetch_agent_card(http: httpx.AsyncClient, endpoint: str) -> dict[str, Any] | None:
    """Fetch an agent's A2A card, or None on failure."""
    url = endpoint.rstrip("/") + "/.well-known/agent-card.json"
    try:
        resp = await http.get(url, timeout=5.0)
        resp.raise_for_status()
        return resp.json()
    except Exception as e:
        log.warning("failed to fetch agent card at %s: %s", url, e)
        return None


def format_agents_for_prompt(agents: list[dict[str, Any]], cards: dict[str, dict[str, Any]]) -> str:
    """Render the available-agents section for the LLM prompt."""
    lines = []
    for i, agent in enumerate(agents, start=1):
        handle = agent.get("handle", "")
        if not handle or handle in SKIP_HANDLES:
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


# ── LLM prompts ───────────────────────────────────────────────────

ORCHESTRATOR_PROMPT = """\
You are the orchestrator for a multi-agent swarm. You coordinate specialist
agents to answer compound queries.

You will receive either:
  1. A user's original question (first turn)
  2. A result from a specialist agent you previously dispatched to (subsequent turns)

On each turn, decide your next action:

  A) DISPATCH to a specialist: return JSON with the agent's handle and a
     standalone query for that agent. The query must be self-contained —
     include any concrete details from prior results (e.g., "Austin, TX"
     not "the delivery destination").

     {{"mentions": ["agent-handle.test"], "content": "standalone query for this agent"}}

  B) ANSWER the user: if you have enough information, return a plain-text
     answer that synthesizes everything. Do NOT wrap it in JSON. Do not
     mention agents or the orchestration process.

Rules:
  - Use atproto handles (e.g., "agent-one.test"), not display names.
  - One dispatch per turn. The bridge will send the result back to you.
  - Order dispatches so later ones can use earlier results.
  - When you have all the information needed, synthesize and answer directly.
  - Keep answers concise."""


DIRECT_ANSWER_PROMPT = """\
You are a general-purpose assistant in a multi-agent swarm. You handle
questions and requests that don't fit any specialist agent.

Be helpful, concise, and honest about your limitations. If a question
seems like it should go to a specialist (e.g., a weather agent, a delivery
tracking agent), mention that — but still do your best to help.

Keep answers brief — a few sentences unless the user asks for detail."""


# ── Helpers ────────────────────────────────────────────────────────


def extract_thread_context(context: RequestContext) -> dict[str, str] | None:
    """Extract thread context from the A2A message's data part."""
    from google.protobuf.json_format import MessageToDict

    msg = context.message
    if not msg or not msg.parts:
        return None
    for part in msg.parts:
        if part.HasField("data"):
            data = MessageToDict(part.data)
            if isinstance(data, dict) and "threadContext" in data:
                return data["threadContext"]
    return None


def parse_dispatch_instruction(text: str) -> dict | None:
    """Try to parse LLM output as a dispatch instruction (JSON with mentions + content)."""
    trimmed = text.strip()
    if not trimmed or trimmed[0] != "{":
        return None
    try:
        parsed = json.loads(trimmed)
    except json.JSONDecodeError:
        m = re.search(r"\{.*\}", trimmed, re.DOTALL)
        if not m:
            return None
        try:
            parsed = json.loads(m.group())
        except json.JSONDecodeError:
            return None
    mentions = parsed.get("mentions", [])
    content = parsed.get("content", "")
    if mentions and content:
        return {"mentions": [m.lstrip("@") for m in mentions], "content": content}
    return None


async def publish_signal(
    http: httpx.AsyncClient,
    publish_url: str,
    handle: str,
    signal_type: str,
    content: str,
    mentions: list[str] | None = None,
    a2a: dict | None = None,
    reply: dict | None = None,
) -> dict | None:
    """Publish a signal via the bridge's POST /publish endpoint."""
    body: dict[str, Any] = {
        "handle": handle,
        "type": signal_type,
        "content": content,
    }
    if mentions:
        body["mentions"] = mentions
    if a2a:
        body["a2a"] = a2a
    if reply:
        body["reply"] = reply
    try:
        resp = await http.post(publish_url, json=body, timeout=10.0)
        resp.raise_for_status()
        return resp.json()
    except Exception as e:
        log.error("publish failed: %s", e)
        return None


# ── A2A executor ──────────────────────────────────────────────────


async def build_chat_history(context: RequestContext, task_store: InMemoryTaskStore) -> list[dict[str, str]]:
    """Build an OpenAI-style message list from prior tasks in the same context."""
    messages: list[dict[str, str]] = []

    context_id = context.context_id
    if not context_id:
        return messages

    from a2a.types import a2a_pb2
    response = await task_store.list(a2a_pb2.ListTasksRequest(context_id=context_id))

    for task in response.tasks:
        for msg in task.history:
            role = "assistant" if msg.role == 2 else "user"  # 2 = ROLE_AGENT
            text_parts = [part.text for part in msg.parts if part.text]
            if text_parts:
                messages.append({"role": role, "content": "\n".join(text_parts)})

    return messages


class OrchestratorExecutor(AgentExecutor):
    def __init__(self, task_store: InMemoryTaskStore):
        self.task_store = task_store
        # Cache for agent info within the same process lifetime.
        self._agents_section: str | None = None
        # Store thread context from the initial dispatch. Keyed by context_id.
        self._thread_contexts: dict[str, dict[str, str]] = {}
        # Track orchestrator's own DID (needed for a2a on /publish signals).
        self._own_did: str | None = None
        self._own_handle: str = os.getenv("ORCHESTRATOR_HANDLE", "orchestrator.test")

    async def execute(self, context: RequestContext, event_queue: EventQueue) -> None:
        updater = TaskUpdater(event_queue, context.task_id, context.context_id)
        user_input = (context.get_user_input() or "").strip()
        log.info("◀ received: %s (task_id=%s, context_id=%s)", user_input[:200], context.task_id, context.context_id)

        if not user_input:
            await updater.complete(new_agent_text_message("I didn't receive a message. How can I help?"))
            return

        # Extract and cache thread context from the initial dispatch.
        tc = extract_thread_context(context)
        if tc and context.context_id:
            self._thread_contexts[context.context_id] = tc
            log.info("thread context received: %s", {k: v[:60] if isinstance(v, str) else v for k, v in tc.items()})

        # Look up cached thread context for this context_id.
        thread_ctx = self._thread_contexts.get(context.context_id) if context.context_id else None

        # Build conversation history from prior turns in this context.
        history = await build_chat_history(context, self.task_store)

        # Discover agents.
        agents_section = await self._get_agents_section()

        if not agents_section or not thread_ctx:
            # No specialists available or no thread context — answer directly.
            reply = await self._direct_answer(history, user_input)
            log.info("▶ direct reply: %s", reply[:200])
            await updater.complete(new_agent_text_message(reply))
            return

        # Orchestrate: call LLM with full history to decide next action.
        reply = await self._orchestrate(history, user_input, agents_section)
        log.info("▶ LLM reply: %s", reply[:200])

        # Check if it's a dispatch instruction.
        instruction = parse_dispatch_instruction(reply)
        if instruction is None:
            # Final answer — return it. The bridge publishes it as a reply signal.
            await updater.complete(new_agent_text_message(reply))
            return

        # Dispatch via /publish. The bridge will dispatch to the specialist
        # and forward the result back to us (same contextId).
        publish_url = thread_ctx.get("publishUrl", f"{BRIDGE_DISCOVERY_URL}/publish")

        # Resolve own DID for a2a if not yet known.
        if not self._own_did:
            self._own_did = await self._resolve_own_did()

        a2a_ref = {
            "agentDid": self._own_did or "",
            "contextId": context.context_id or "",
        }

        reply_ref = {
            "parentUri": thread_ctx["signalUri"],
            "parentCid": thread_ctx["signalCid"],
            "rootUri": thread_ctx["rootUri"],
            "rootCid": thread_ctx["rootCid"],
        }

        log.info("publishing dispatch: mentions=%s content=%s", instruction["mentions"], instruction["content"][:100])

        async with httpx.AsyncClient() as http:
            result = await publish_signal(
                http,
                publish_url,
                self._own_handle,
                "routing",
                instruction["content"],
                mentions=instruction["mentions"],
                a2a=a2a_ref,
                reply=reply_ref,
            )

        if result:
            # Update thread context: the /publish signal is now the latest
            # parent for subsequent signals in this thread.
            thread_ctx["signalUri"] = result["uri"]
            thread_ctx["signalCid"] = result["cid"]
            log.info("dispatch published: uri=%s", result.get("uri"))

        # Return a brief status. The bridge publishes this as a result signal
        # (which is skipped on the firehose — it's just an audit record).
        # The real work continues when the specialist result arrives.
        await updater.complete(
            new_agent_text_message(f"Dispatching to {', '.join(instruction['mentions'])}...")
        )

    async def _get_agents_section(self) -> str:
        """Discover agents and format for prompt. Cached per process."""
        if self._agents_section is not None:
            return self._agents_section

        async with httpx.AsyncClient() as http:
            agents = await discover_agents(http)
            if not agents:
                self._agents_section = ""
                return ""

            card_results = await asyncio.gather(
                *(fetch_agent_card(http, a["a2aEndpoint"]) for a in agents),
                return_exceptions=False,
            )
            cards = {a["handle"]: c for a, c in zip(agents, card_results) if c}

        self._agents_section = format_agents_for_prompt(agents, cards)
        return self._agents_section

    async def _orchestrate(self, history: list[dict], user_input: str, agents_section: str) -> str:
        """Use LLM to decide: dispatch to a specialist or answer directly."""
        system = (
            f"{ORCHESTRATOR_PROMPT}\n\n"
            f'Available specialist agents (use the @handle in "mentions"):\n\n'
            f"{agents_section}"
        )

        messages: list[dict[str, str]] = [{"role": "system", "content": system}]
        messages.extend(history)
        messages.append({"role": "user", "content": user_input})

        log.info("calling LLM (%d messages including system)", len(messages))
        try:
            completion = await llm_client.chat.completions.create(
                model=LLM_MODEL,
                messages=messages,
                temperature=0.2,
            )
            reply = (completion.choices[0].message.content or "").strip()
            log.info("LLM response: %s", reply[:300])
            return reply
        except Exception as e:
            log.error("LLM call failed: %s", e)
            return f"Sorry, I'm having trouble right now. ({e})"

    async def _direct_answer(self, history: list[dict], user_input: str) -> str:
        """Answer directly — no specialists available."""
        messages: list[dict[str, str]] = [{"role": "system", "content": DIRECT_ANSWER_PROMPT}]
        messages.extend(history)
        messages.append({"role": "user", "content": user_input})

        log.info("direct answer: sending %d messages to LLM", len(messages))
        try:
            completion = await llm_client.chat.completions.create(
                model=LLM_MODEL,
                messages=messages,
                temperature=0.5,
            )
            return (completion.choices[0].message.content or "").strip()
        except Exception as e:
            log.error("LLM call failed: %s", e)
            return f"Sorry, I'm having trouble generating a response right now. ({e})"

    async def _resolve_own_did(self) -> str:
        """Resolve the orchestrator's DID by checking the bridge's agent list."""
        async with httpx.AsyncClient() as http:
            agents = await discover_agents(http)
            for a in agents:
                if a.get("handle") == self._own_handle:
                    return a.get("did", "")
        return ""

    async def cancel(self, context: RequestContext, event_queue: EventQueue) -> None:
        pass


# ── Agent card and app setup ──────────────────────────────────────


def build_app():
    agent_card = AgentCard(
        name="orchestrator_agent",
        description=(
            "General-purpose assistant and orchestrator. Handles questions that don't "
            "fit a specialist, and coordinates multi-agent queries by decomposing them "
            "into sub-tasks dispatched to specialist agents via /publish."
        ),
        supported_interfaces=[
            AgentInterface(url=f"http://localhost:{PORT}", protocol_binding="JSONRPC"),
        ],
        version="0.3.0",
        default_input_modes=["text/plain"],
        default_output_modes=["text/plain"],
        capabilities=AgentCapabilities(streaming=False),
        skills=[
            AgentSkill(
                id="general_assistant",
                name="General Assistant",
                description=(
                    "Handles general questions, ambiguous requests, and anything that "
                    "doesn't fit a specialist agent. Answers directly."
                ),
                tags=["general", "chat", "fallback"],
                examples=[
                    "Tell me a joke",
                    "What's the capital of France?",
                    "Help me brainstorm ideas for a birthday party",
                ],
            ),
            AgentSkill(
                id="multi_agent_coordinator",
                name="Multi-Agent Coordinator",
                description=(
                    "Decomposes compound queries that need multiple specialist agents, "
                    "dispatches sub-tasks in the right order via /publish, and synthesizes "
                    "results into a single coherent answer."
                ),
                tags=["orchestrator", "coordinator", "decompose"],
                examples=[
                    "When does my package arrive and what's the weather there?",
                    "Track my delivery and tell me if I need an umbrella when it arrives",
                ],
            ),
        ],
    )

    task_store = InMemoryTaskStore(owner_resolver=lambda _: "default")
    executor = OrchestratorExecutor(task_store)
    handler = DefaultRequestHandler(
        agent_executor=executor,
        task_store=task_store,
    )

    server = A2AStarletteApplication(
        agent_card=agent_card,
        http_handler=handler,
        enable_v0_3_compat=True,
    )
    return server.build()


app = build_app()


if __name__ == "__main__":
    log.info("orchestrator agent starting on port %d", PORT)
    log.info("  LLM base URL: %s", LLM_BASE_URL)
    log.info("  LLM model:    %s", LLM_MODEL)
    log.info("  bridge discovery: %s", BRIDGE_DISCOVERY_URL)
    uvicorn.run(app, host="0.0.0.0", port=PORT)
