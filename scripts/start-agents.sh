#!/usr/bin/env bash
# Start all murmur A2A agents in one terminal. Ctrl-C kills them all cleanly.
#
# The delivery agent lives in agent-skill-poc; the swarm-native agents (triage,
# weather, orchestrator, threeaday) live in a sibling repo under
# ~/projects/agents/. Each agent gets a `uv sync` on first run (fast no-op
# after that) so you don't need to prep each venv separately.
#
# Override locations with:
#   AGENT_SKILL_POC_DIR=/path/to/agent-skill-poc
#   AGENTS_DIR=/path/to/agents
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

AGENT_SKILL_POC="${AGENT_SKILL_POC_DIR:-$HOME/projects/agent-skill-poc}"
AGENTS_DIR="${AGENTS_DIR:-$HOME/projects/agents}"

start_agent() {
  local name="$1"
  local port="$2"
  local dir="$3"
  local entry="$4"

  if [ ! -d "$dir" ]; then
    echo "  ⚠ $name skipped — $dir not found"
    return
  fi

  (cd "$dir" && uv sync --quiet && uv run python "$entry") &
  PIDS+=($!)
  echo "  $name (port $port) — PID $!"
}

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

start_agent "triage-agent"       5050 "$AGENTS_DIR/triage"       triage_agent.py
start_agent "weather-agent"      5051 "$AGENTS_DIR/weather"      weather_agent.py
start_agent "orchestrator-agent" 5052 "$AGENTS_DIR/orchestrator" orchestrator_agent.py
start_agent "threeaday-agent"    5053 "$AGENTS_DIR/threeaday"    threeaday_agent.py

echo ""
echo "All agents running. Press Ctrl-C to stop."
wait
