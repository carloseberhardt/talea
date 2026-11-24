Terminal 1 — Docker stack
cd ~/projects/talea
docker compose up -d --build
Verify with docker compose ps, curl -s https://pds.talea.internal/xrpc/_health, and curl -s
http://localhost:2470/xrpc/\_health.

Terminal 2 — Agents (delivery, triage, weather, orchestrator, threeaday)
cd ~/projects/talea
./scripts/start-agents.sh
Ctrl-C kills them all. Triage on :5050, weather :5051, orchestrator :5052, threeaday :5053, delivery :5006.

Terminal 3 — AppView (start this before the bridge)
cd ~/projects/talea
./appview/appview
Listens on :2472. Wait for appview listening + firehose subscriber connecting before starting the bridge — the
bridge warm-starts its dispatch registry from GET /api/agents, and starting AppView first avoids a cold start.

Terminal 4 — Bridge
cd ~/projects/talea
./bridge/bridge
Watch for authenticated as bridge, registry backfill complete (from appview directory), and connected to firehose.
If AppView wasn't up yet, you'll see a few retry warnings and then it proceeds with an empty registry that fills in
from the live firehose — recoverable, just less clean.

Terminal 5 — Web client (new — was bundled into AppView before)
cd ~/projects/talea
python3 -m http.server 3000 --directory webui
Any static server works; this is the dev shortcut. Demo URL for your friends is now http://localhost:3000 (not
2472). The browser app reads webui/config.js and calls the AppView's API at http://localhost:2472. Both must be
reachable from the browser.

Terminal 6 (or on-demand) — CLI
cd ~/projects/talea
./cli/talea agents # confirm 5 agents in the directory
./cli/talea post "where is package 241234" # triage → delivery
./cli/talea post "what's the weather in Austin" # triage → weather
./cli/talea post "tell me a joke" # triage → orchestrator
./cli/talea post "@weather-one.test @agent-one.test how are you?" # bridge fans out, no triage

Pre-demo checklist:

- Go binaries built: `./appview/appview` and `./bridge/bridge` both exist. If not:
  `cd appview && go build -o appview ./cmd/appview && cd ../bridge && go build -o bridge ./cmd/bridge`
- .env at project root has WATSONX_API_KEY, WATSONX_PROJECT_ID, TALEA_AGENT_CREDS
- /etc/hosts has 127.0.0.1 pds.talea.internal
- ./cli/talea agents shows all five; if not, ./cli/talea publish-profile --file scripts/profiles.json
- webui/config.js has apiBase: "http://localhost:2472" (default — only change if you front it with a reverse proxy)
- Browser doesn't have an old AppView page cached at :2472; tell friends to open :3000
