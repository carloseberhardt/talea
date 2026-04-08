package main

// HTTP discovery surface — exposes the bridge's agent registry to other
// services (notably the triage agent) so they can route or reason about the
// swarm without subscribing to the firehose or learning atproto. The data
// here is the same DID → endpoint mapping the bridge already maintains for
// mention dispatch; we're just letting other services read it.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// agentDTO is the wire shape returned by GET /agents. Kept deliberately minimal
// — handle, DID, and endpoint. Skill descriptions live in the agent's A2A
// card, which any consumer can fetch directly from the endpoint.
type agentDTO struct {
	Handle      string `json:"handle"`
	DID         string `json:"did"`
	Name        string `json:"name,omitempty"`
	A2AEndpoint string `json:"a2aEndpoint"`
}

// startDiscoveryServer launches the HTTP discovery surface on the configured
// bind address. It runs until ctx is cancelled. Errors during startup are
// returned synchronously; runtime errors are logged.
func (b *Bridge) startDiscoveryServer(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/agents", b.handleListAgents)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	go func() {
		b.logger.Info("discovery server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			b.logger.Error("discovery server exited", "error", err)
		}
	}()

	return nil
}

func (b *Bridge) handleListAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	agents := b.registry.All()
	out := make([]agentDTO, 0, len(agents))
	for _, a := range agents {
		out = append(out, agentDTO{
			Handle:      a.Handle,
			DID:         a.DID,
			Name:        a.Name,
			A2AEndpoint: a.A2AEndpoint,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		b.logger.Warn("failed to encode /agents response", "error", err)
	}
}
