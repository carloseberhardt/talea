package main

import (
	"log/slog"
	"strings"
	"sync"
)

// RegisteredAgent represents an agent the bridge knows about and can route to.
type RegisteredAgent struct {
	DID         string   // atproto DID of the agent's PDS account
	Name        string   // human-readable name
	A2AEndpoint string   // URL for A2A task dispatch
	Tags        []string // capability/skill tags for matching
}

// AgentRegistry maintains a mapping from capability tags to agents.
// For now this is populated from config. Later, the bridge will
// discover agents dynamically from community.swarm.agent.profile records.
type AgentRegistry struct {
	mu     sync.RWMutex
	agents []RegisteredAgent
	logger *slog.Logger
}

func NewAgentRegistry(logger *slog.Logger) *AgentRegistry {
	return &AgentRegistry{
		logger: logger.With("component", "registry"),
	}
}

// Register adds an agent to the registry.
func (r *AgentRegistry) Register(agent RegisteredAgent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents = append(r.agents, agent)
	r.logger.Info("agent registered", "name", agent.Name, "endpoint", agent.A2AEndpoint, "tags", agent.Tags)
}

// FindByCapability returns all agents that match any of the given capability tags.
func (r *AgentRegistry) FindByCapability(capabilities []string) []RegisteredAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var matches []RegisteredAgent
	for _, agent := range r.agents {
		if tagsOverlap(agent.Tags, capabilities) {
			matches = append(matches, agent)
		}
	}
	return matches
}

func tagsOverlap(a, b []string) bool {
	for _, tagA := range a {
		for _, tagB := range b {
			if strings.EqualFold(tagA, tagB) {
				return true
			}
		}
	}
	return false
}
