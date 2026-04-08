# Triage agent

The first "thinking" agent in murmur. The bridge calls this over A2A when a
signal arrives without explicit @-mentions and without an obvious chat-
continuation parent. Triage decides which agents (if any) should handle the
signal and returns a routing decision the bridge can act on.

## What it does

1. Receives an A2A `SendMessage` containing a user's signal text.
2. Discovers available agents from the bridge's HTTP `/agents` endpoint.
3. Fetches each agent's A2A card directly from the agent's endpoint to get
   rich skill descriptions.
4. Asks an LLM (via AgentGateway) which agents should handle the signal.
5. Returns a JSON object: `{"mentions": ["handle1.test", ...], "rationale": "..."}`.

The agent is **atproto-naive** by design — it never talks to atproto. The
bridge does all atproto work and exposes what triage needs over plain HTTP.

## Run it

```bash
cd agents/triage
uv sync
uv run python triage_agent.py
```

Serves A2A on port `5050` by default.

## Environment variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `TRIAGE_PORT` | `5050` | A2A listener port |
| `BRIDGE_DISCOVERY_URL` | `http://localhost:2471` | Where to query for the agent registry |
| `LLM_BASE_URL` | `http://localhost:4000/watsonx` | OpenAI-compatible LLM endpoint (AgentGateway route) |
| `LLM_MODEL` | `openai/gpt-oss-120b` | Model identifier passed to the LLM |

## Smoke test

With the bridge, AgentGateway, and at least one domain agent registered, you
can hit triage directly with curl:

```bash
curl -s -X POST http://localhost:5050/ \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": "1",
    "method": "message/send",
    "params": {
      "message": {
        "messageId": "test-1",
        "role": "user",
        "parts": [{"kind": "text", "text": "where is package 241234?"}]
      }
    }
  }' | jq
```

You should see a response containing the JSON routing decision in the message
parts.

## What it does not do

- It does **not** publish to atproto. The bridge takes triage's response and
  publishes a routing reply on its behalf.
- It does **not** subscribe to the firehose. It only acts when called.
- It does **not** maintain state between calls. Each request is independent.

These properties keep triage simple, swappable, and portable to any A2A-aware
host.
