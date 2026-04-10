"""
Orchestrator agent — general-purpose chat for signals no specialist handles.

The fallback agent in the murmur swarm. Triage routes here when no specialist
domain agent is a good fit for a signal. In this first incarnation the
orchestrator is a simple LLM-backed chat agent — it answers directly rather
than decomposing or coordinating sub-tasks. Decomposition (dispatch + watch)
is a future evolution.

Run: uv run python orchestrator_agent.py
Serves on port 5052 by default (override with ORCHESTRATOR_PORT).
"""

import logging
import os

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

PORT = int(os.getenv("ORCHESTRATOR_PORT", "5052"))
LLM_BASE_URL = os.getenv("LLM_BASE_URL", "http://localhost:4000/watsonx")
LLM_MODEL = os.getenv("LLM_MODEL", "openai/gpt-oss-120b")

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(name)s] %(levelname)s: %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("orchestrator")

llm_client = AsyncOpenAI(base_url=LLM_BASE_URL, api_key="not-needed")

# ── LLM prompt ─────────────────────────────────────────────────────

SYSTEM_PROMPT = """\
You are a general-purpose assistant in a multi-agent swarm. You handle
questions and requests that don't fit any specialist agent.

Be helpful, concise, and honest about your limitations. If a question
seems like it should go to a specialist (e.g., a weather agent, a delivery
tracking agent), mention that — but still do your best to help.

Keep answers brief — a few sentences unless the user asks for detail."""


# ── A2A executor ────────────────────────────────────────────────────


class OrchestratorExecutor(AgentExecutor):
    async def execute(self, context: RequestContext, event_queue: EventQueue) -> None:
        user_input = (context.get_user_input() or "").strip()
        log.info("◀ received: %s", user_input[:200])

        if not user_input:
            await event_queue.enqueue_event(
                new_agent_text_message("I didn't receive a message. How can I help?")
            )
            return

        try:
            completion = await llm_client.chat.completions.create(
                model=LLM_MODEL,
                messages=[
                    {"role": "system", "content": SYSTEM_PROMPT},
                    {"role": "user", "content": user_input},
                ],
                temperature=0.5,
            )
            reply = (completion.choices[0].message.content or "").strip()
        except Exception as e:
            log.error("LLM call failed: %s", e)
            reply = f"Sorry, I'm having trouble generating a response right now. ({e})"

        log.info("▶ reply: %s", reply[:200])
        await event_queue.enqueue_event(new_agent_text_message(reply))

    async def cancel(self, context: RequestContext, event_queue: EventQueue) -> None:
        pass


# ── Agent card and app setup ────────────────────────────────────────


def build_app():
    agent_card = AgentCard(
        name="orchestrator_agent",
        description=(
            "General-purpose assistant that handles questions and requests which don't "
            "fit any specialist agent. The swarm's fallback — if no domain agent is the "
            "right fit, the orchestrator steps in."
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
        ],
    )

    executor = OrchestratorExecutor()
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
    log.info("orchestrator agent starting on port %d", PORT)
    log.info("  LLM base URL: %s", LLM_BASE_URL)
    log.info("  LLM model:    %s", LLM_MODEL)
    uvicorn.run(app, host="0.0.0.0", port=PORT)
