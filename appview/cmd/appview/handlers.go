package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// registerHandlers wires all HTTP routes onto mux.
func registerHandlers(mux *http.ServeMux, state *State, hub *Hub, poster *Poster, discoveryURL string, logger *slog.Logger) {
	mux.HandleFunc("/api/threads", func(w http.ResponseWriter, r *http.Request) {
		handleThreads(w, r, state)
	})
	mux.HandleFunc("/api/thread", func(w http.ResponseWriter, r *http.Request) {
		handleThread(w, r, state)
	})
	mux.HandleFunc("/api/agents", func(w http.ResponseWriter, r *http.Request) {
		handleAgents(w, r, discoveryURL, logger)
	})
	mux.HandleFunc("/api/agent", func(w http.ResponseWriter, r *http.Request) {
		handleAgent(w, r, state)
	})
	mux.HandleFunc("/api/agent-card", func(w http.ResponseWriter, r *http.Request) {
		handleAgentCard(w, r, discoveryURL, logger)
	})
	mux.HandleFunc("/api/post", func(w http.ResponseWriter, r *http.Request) {
		handlePost(w, r, poster, logger)
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWS(hub, logger, w, r)
	})
}

func setCORSAndJSON(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
}

func writeJSON(w http.ResponseWriter, v any) {
	setCORSAndJSON(w)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Response headers already sent; can't change status.
		return
	}
}

func writeError(w http.ResponseWriter, msg string, code int) {
	setCORSAndJSON(w)
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// GET /api/threads[?agent=handle]
func handleThreads(w http.ResponseWriter, r *http.Request, state *State) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent := r.URL.Query().Get("agent")
	summaries := state.GetThreadSummaries(agent)
	if summaries == nil {
		summaries = []ThreadSummary{}
	}
	writeJSON(w, summaries)
}

// GET /api/thread?root=<uri>
func handleThread(w http.ResponseWriter, r *http.Request, state *State) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rootURI := r.URL.Query().Get("root")
	if rootURI == "" {
		writeError(w, "missing root parameter", http.StatusBadRequest)
		return
	}
	signals := state.GetThread(rootURI)
	if signals == nil {
		signals = []*Signal{}
	}
	writeJSON(w, signals)
}

// GET /api/agents — proxy to bridge discovery
func handleAgents(w http.ResponseWriter, r *http.Request, discoveryURL string, logger *slog.Logger) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp, err := http.Get(discoveryURL + "/agents")
	if err != nil {
		logger.Warn("failed to proxy /agents request", "error", err)
		writeError(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	setCORSAndJSON(w)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// GET /api/agent?handle=<handle>
func handleAgent(w http.ResponseWriter, r *http.Request, state *State) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	handle := r.URL.Query().Get("handle")
	if handle == "" {
		writeError(w, "missing handle parameter", http.StatusBadRequest)
		return
	}
	signals := state.GetAuthorSignals(handle)
	if signals == nil {
		signals = []*Signal{}
	}
	writeJSON(w, signals)
}

// GET /api/agent-card?endpoint=<a2a_endpoint>
func handleAgentCard(w http.ResponseWriter, r *http.Request, discoveryURL string, logger *slog.Logger) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	endpoint := r.URL.Query().Get("endpoint")
	if endpoint == "" {
		writeError(w, "missing endpoint parameter", http.StatusBadRequest)
		return
	}
	cardURL := endpoint + "/.well-known/agent-card.json"
	resp, err := http.Get(cardURL)
	if err != nil {
		logger.Warn("failed to fetch agent card", "url", cardURL, "error", err)
		writeError(w, "failed to fetch agent card", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	setCORSAndJSON(w)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// POST /api/post
func handlePost(w http.ResponseWriter, r *http.Request, poster *Poster, logger *slog.Logger) {
	if r.Method != http.MethodPost {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Content  string   `json:"content"`
		Mentions []string `json:"mentions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		writeError(w, "content is required", http.StatusBadRequest)
		return
	}

	uri, cid, err := poster.Post(r.Context(), req.Content, req.Mentions)
	if err != nil {
		logger.Warn("post failed", "error", err)
		writeError(w, "failed to create signal: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"uri": uri, "cid": cid})
}
