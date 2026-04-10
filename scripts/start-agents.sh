#!/usr/bin/env bash
# Start all murmur A2A agents in one terminal. Ctrl-C kills them all cleanly.
#
# The delivery agent lives in the agent-skill-poc project; the rest are in
# this repo under agents/. Each agent gets a `uv sync` on first run (fast
# no-op after that) so you don't need to prep each venv separately.
set -e

PIDS=()

cleanup() {
    echo ""
    echo "Stopping agents..."
    for pid in "${PIDS[@]}"; do
        kill "$pid" 2>/dev/null && wait "$pid" 2>/dev/null
    done
    echo "All agents stopped."
}
trap cleanup EXIT INT TERM

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
AGENT_SKILL_POC="${AGENT_SKILL_POC_DIR:-$HOME/projects/agent-skill-poc}"

echo "Starting murmur A2A agents..."

# Delivery agent (from agent-skill-poc)
if [ -d "$AGENT_SKILL_POC" ]; then
    (cd "$AGENT_SKILL_POC" && uv run python mock-agents/delivery_agent.py) &
    PIDS+=($!)
    echo "  delivery-agent (port 5006) — PID $!"
else
    echo "  ⚠ delivery-agent skipped — $AGENT_SKILL_POC not found"
    echo "    Set AGENT_SKILL_POC_DIR to override"
fi

# Triage agent
(cd "$PROJECT_DIR/agents/triage" && uv sync --quiet && uv run python triage_agent.py) &
PIDS+=($!)
echo "  triage-agent (port 5050) — PID $!"

# Weather agent
(cd "$PROJECT_DIR/agents/weather" && uv sync --quiet && uv run python weather_agent.py) &
PIDS+=($!)
echo "  weather-agent (port 5051) — PID $!"

# Orchestrator agent
(cd "$PROJECT_DIR/agents/orchestrator" && uv sync --quiet && uv run python orchestrator_agent.py) &
PIDS+=($!)
echo "  orchestrator-agent (port 5052) — PID $!"

echo ""
echo "All agents running. Press Ctrl-C to stop."
wait
