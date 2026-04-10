"""
Weather agent — answers weather-related questions via LLM.

A domain agent in the murmur swarm. Triage routes weather questions here.
The agent calls an LLM (via AgentGateway) to generate responses.

Run: uv run python weather_agent.py
Serves on port 5051 by default (override with WEATHER_PORT).
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

PORT = int(os.getenv("WEATHER_PORT", "5051"))
LLM_BASE_URL = os.getenv("LLM_BASE_URL", "http://localhost:4000/watsonx")
LLM_MODEL = os.getenv("LLM_MODEL", "openai/gpt-oss-120b")

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(name)s] %(levelname)s: %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("weather")

llm_client = AsyncOpenAI(base_url=LLM_BASE_URL, api_key="not-needed")

# ── LLM prompt ─────────────────────────────────────────────────────

SYSTEM_PROMPT = """\
You are a weather information agent. Answer questions about weather conditions,
forecasts, and climate. Be concise and helpful.

If the question is not weather-related, say so politely and suggest that
the user ask a more appropriate agent.

Keep answers brief — a few sentences at most."""


# ── A2A executor ────────────────────────────────────────────────────


class WeatherExecutor(AgentExecutor):
    async def execute(self, context: RequestContext, event_queue: EventQueue) -> None:
        user_input = (context.get_user_input() or "").strip()
        log.info("◀ received: %s", user_input[:200])

        if not user_input:
            await event_queue.enqueue_event(
                new_agent_text_message("I didn't receive a question. What would you like to know about the weather?")
            )
            return

        try:
            completion = await llm_client.chat.completions.create(
                model=LLM_MODEL,
                messages=[
                    {"role": "system", "content": SYSTEM_PROMPT},
                    {"role": "user", "content": user_input},
                ],
                temperature=0.3,
            )
            reply = (completion.choices[0].message.content or "").strip()
        except Exception as e:
            log.error("LLM call failed: %s", e)
            reply = f"Sorry, I'm having trouble reaching my weather data source right now. ({e})"

        log.info("▶ reply: %s", reply[:200])
        await event_queue.enqueue_event(new_agent_text_message(reply))

    async def cancel(self, context: RequestContext, event_queue: EventQueue) -> None:
        pass


# ── Agent card and app setup ────────────────────────────────────────


def build_app():
    agent_card = AgentCard(
        name="weather_agent",
        description=(
            "Answers questions about weather conditions, forecasts, and climate. "
            "Powered by an LLM — responses are informational, not sourced from a live API."
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
                id="weather_info",
                name="Weather Information",
                description=(
                    "Provides weather forecasts, current conditions, and general climate "
                    "information for any location."
                ),
                tags=["weather", "forecast", "climate"],
                examples=[
                    "What's the weather like in Austin TX?",
                    "Will it rain in Seattle this weekend?",
                    "What's the average temperature in Tokyo in April?",
                ],
            ),
        ],
    )

    executor = WeatherExecutor()
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
    log.info("weather agent starting on port %d", PORT)
    log.info("  LLM base URL: %s", LLM_BASE_URL)
    log.info("  LLM model:    %s", LLM_MODEL)
    uvicorn.run(app, host="0.0.0.0", port=PORT)
