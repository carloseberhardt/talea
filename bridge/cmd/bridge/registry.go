package main

import (
	"log/slog"
	"strings"
	"sync"
)

// RegisteredAgent represents an agent the bridge knows about and can route to.
// The bridge keeps just enough to dispatch by @-mention: handle/DID resolution
// and the A2A endpoint to dispatch to. Skill descriptions, tags, and richer
// metadata live in the agent's A2A card and are fetched on demand by whoever
// needs them (e.g., the swarm agent for routing decisions).
type RegisteredAgent struct {
	DID         string // atproto DID of the agent's PDS account
	Name        string // human-readable name (kept for logging)
	Handle      string // atproto handle (e.g., "agent-one.test")
	A2AEndpoint string // URL for A2A task dispatch

	// ManagedByBridge is true if the bridge minted this agent's atproto identity
	// and is responsible for its profile lifecycle (the gateway-as-onramp future
	// described in IMPLEMENTATION.md). False means the profile was self-published
	// to atproto by the agent's owner. Today nothing is bridge-managed; the field
	// is forward-compatibility insurance so we can distinguish the two origins
	// once the gateway-as-onramp reconciler exists.
	ManagedByBridge bool
}

// AgentRegistry maintains a DID → endpoint cache used for @-mention dispatch.
// Agents are discovered from community.swarm.agent.profile records on the
// firehose (live updates) and via PDS startup backfill.
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
		r.logger.Info("agent updated", "name", agent.Name, "did", agent.DID, "handle", agent.Handle, "endpoint", agent.A2AEndpoint)
	} else {
		r.logger.Info("agent registered", "name", agent.Name, "did", agent.DID, "handle", agent.Handle, "endpoint", agent.A2AEndpoint)
		existing = &RegisteredAgent{}
	}

	existing.DID = agent.DID
	existing.Name = agent.Name
	existing.A2AEndpoint = agent.A2AEndpoint
	existing.ManagedByBridge = agent.ManagedByBridge
	if agent.Handle != "" {
		existing.Handle = agent.Handle
	}
	r.agents[agent.DID] = existing
}

// Remove drops an agent from the registry by DID. Returns true if the agent
// was present.
func (r *AgentRegistry) Remove(did string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	agent, ok := r.agents[did]
	if !ok {
		return false
	}
	delete(r.agents, did)
	r.logger.Info("agent removed", "name", agent.Name, "did", did)
	return true
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

