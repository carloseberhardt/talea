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
	Handle      string   // atproto handle (e.g., "agent-one.test")
	A2AEndpoint string   // URL for A2A task dispatch
	Tags        []string // capability/skill tags for matching
}

// AgentRegistry maintains a mapping from capability tags to agents.
// Agents are discovered dynamically from community.swarm.agent.profile records
// on the firehose, and optionally enriched from their A2A agent cards.
type AgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]*RegisteredAgent // keyed by DID
	logger *slog.Logger
}

func NewAgentRegistry(logger *slog.Logger) *AgentRegistry {
	return &AgentRegistry{
		agents: make(map[string]*RegisteredAgent),
		logger: logger.With("component", "registry"),
	}
}

// Upsert adds or updates an agent in the registry.
func (r *AgentRegistry) Upsert(agent RegisteredAgent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.agents[agent.DID]
	if ok {
		r.logger.Info("agent updated", "name", agent.Name, "did", agent.DID, "endpoint", agent.A2AEndpoint, "tags", agent.Tags)
	} else {
		r.logger.Info("agent registered", "name", agent.Name, "did", agent.DID, "endpoint", agent.A2AEndpoint, "tags", agent.Tags)
		existing = &RegisteredAgent{}
	}

	existing.DID = agent.DID
	existing.Name = agent.Name
	existing.A2AEndpoint = agent.A2AEndpoint
	existing.Tags = agent.Tags
	if agent.Handle != "" {
		existing.Handle = agent.Handle
	}
	r.agents[agent.DID] = existing
}

// FindByCapability returns all agents that match any of the given capability tags.
func (r *AgentRegistry) FindByCapability(capabilities []string) []RegisteredAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var matches []RegisteredAgent
	for _, agent := range r.agents {
		if tagsOverlap(agent.Tags, capabilities) {
			matches = append(matches, *agent)
		}
	}
	return matches
}

// FindByDID returns an agent by its atproto DID, or nil if not found.
func (r *AgentRegistry) FindByDID(did string) *RegisteredAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if agent, ok := r.agents[did]; ok {
		cp := *agent
		return &cp
	}
	return nil
}

// FindByHandle returns an agent by its atproto handle, or nil if not found.
func (r *AgentRegistry) FindByHandle(handle string) *RegisteredAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, agent := range r.agents {
		if strings.EqualFold(agent.Handle, handle) {
			cp := *agent
			return &cp
		}
	}
	return nil
}

// All returns a copy of all registered agents.
func (r *AgentRegistry) All() []RegisteredAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]RegisteredAgent, 0, len(r.agents))
	for _, agent := range r.agents {
		all = append(all, *agent)
	}
	return all
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
