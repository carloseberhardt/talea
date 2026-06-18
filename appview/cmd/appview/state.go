package main

import (
	"sort"
	"sync"
	"time"
)

const (
	maxRecentThreads = 100
	maxByAuthor      = 500
	maxDepth         = 10
)

// Signal is a community.swarm.signal record indexed by the appview.
type Signal struct {
	URI          string    `json:"uri"`
	CID          string    `json:"cid"`
	AuthorDID    string    `json:"authorDid"`
	AuthorHandle string    `json:"authorHandle"`
	Type         string    `json:"type"`
	Content      string    `json:"content"`
	Mentions     []string  `json:"mentions,omitempty"`
	Rationale    string    `json:"rationale,omitempty"`
	A2A          *A2AInfo  `json:"a2a,omitempty"`
	ParentURI    string    `json:"parentUri,omitempty"`
	RootURI      string    `json:"rootUri"`
	CreatedAt    time.Time `json:"createdAt"`
	IndexedAt    time.Time `json:"indexedAt"`
	Depth        int       `json:"depth"`
}

// A2AInfo holds A2A back-reference metadata attached to a signal.
type A2AInfo struct {
	AgentDID  string `json:"agentDid"`
	TaskID    string `json:"taskId,omitempty"`
	ContextID string `json:"contextId,omitempty"`
}

// AgentProfile is a community.swarm.agent.profile record indexed by the appview.
// Together these form the swarm's agent directory: discovered from the relay
// firehose (federation-correct, network-wide) and seeded by startup backfill.
// The AppView is the canonical "who's in the swarm" index — queried by the web
// client today and by routing agents (the orchestrator) in a later pass, which
// is why discovery belongs here rather than behind the bridge's dispatch cache.
type AgentProfile struct {
	DID         string    `json:"did"`
	Handle      string    `json:"handle"`
	Name        string    `json:"name,omitempty"`
	Description string    `json:"description,omitempty"`
	A2AEndpoint string    `json:"a2aEndpoint"`
	Status      string    `json:"status,omitempty"`
	IndexedAt   time.Time `json:"indexedAt"`
}

// Thread is an in-memory collection of signals sharing the same root.
type Thread struct {
	RootURI   string
	Signals   []*Signal
	UpdatedAt time.Time
}

// ThreadSummary is the lightweight view sent to the thread list endpoint.
type ThreadSummary struct {
	RootURI      string    `json:"rootUri"`
	Preview      string    `json:"preview"`
	AuthorHandle string    `json:"authorHandle"`
	SignalCount  int       `json:"signalCount"`
	Participants []string  `json:"participants"`
	Mentions     []string  `json:"mentions,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Types        []string  `json:"types"`
}

// State holds the appview's in-memory view of the swarm.
type State struct {
	mu          sync.RWMutex
	threads     map[string]*Thread       // keyed by root URI
	byAuthor    map[string][]*Signal     // keyed by handle
	profiles    map[string]*AgentProfile // agent directory, keyed by DID
	recentRoots []string                 // ring buffer of root URIs, newest first
	handleCache sync.Map                 // DID → handle string
	hub         *Hub
}

// NewState creates an empty State wired to the given Hub for broadcasting.
func NewState(hub *Hub) *State {
	return &State{
		threads:  make(map[string]*Thread),
		byAuthor: make(map[string][]*Signal),
		profiles: make(map[string]*AgentProfile),
		hub:      hub,
	}
}

// AddSignal indexes a new signal and broadcasts it to connected WebSocket
// clients. Duplicates (same URI) are silently ignored — this lets the firehose
// and PDS backfill safely overlap.
func (s *State) AddSignal(sig *Signal) {
	if s.indexSignal(sig) {
		s.hub.BroadcastSignal(sig)
	}
}

// AddSignalSilent indexes a signal without broadcasting it. Used by the
// startup backfill so reconstructed history doesn't appear as live activity
// to connected web clients.
func (s *State) AddSignalSilent(sig *Signal) {
	s.indexSignal(sig)
}

// indexSignal performs the locked indexing work. Returns true if the signal
// was newly added, false if it was a duplicate (same URI already indexed in
// the same thread).
func (s *State) indexSignal(sig *Signal) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	thread, exists := s.threads[sig.RootURI]
	if !exists {
		thread = &Thread{RootURI: sig.RootURI}
		s.threads[sig.RootURI] = thread
	}
	for _, existing := range thread.Signals {
		if existing.URI == sig.URI {
			return false
		}
	}
	thread.Signals = append(thread.Signals, sig)
	thread.UpdatedAt = sig.IndexedAt

	sig.Depth = computeDepth(sig, thread)

	if sig.AuthorHandle != "" {
		existing := s.byAuthor[sig.AuthorHandle]
		existing = append(existing, sig)
		if len(existing) > maxByAuthor {
			existing = existing[len(existing)-maxByAuthor:]
		}
		s.byAuthor[sig.AuthorHandle] = existing
	}

	s.upsertRecentRoot(sig.RootURI)
	return true
}

// upsertRecentRoot moves rootURI to the front of recentRoots, or prepends it.
// Trims to maxRecentThreads. Must be called with s.mu held.
func (s *State) upsertRecentRoot(rootURI string) {
	for i, r := range s.recentRoots {
		if r == rootURI {
			// Move to front.
			s.recentRoots = append([]string{rootURI}, append(s.recentRoots[:i], s.recentRoots[i+1:]...)...)
			return
		}
	}
	s.recentRoots = append([]string{rootURI}, s.recentRoots...)
	if len(s.recentRoots) > maxRecentThreads {
		s.recentRoots = s.recentRoots[:maxRecentThreads]
	}
}

// GetThreadSummaries returns summaries for recent threads, optionally filtered
// by author handle. Results are in recentRoots order (newest-updated first).
func (s *State) GetThreadSummaries(filterHandle string) []ThreadSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	summaries := make([]ThreadSummary, 0, len(s.recentRoots))
	for _, rootURI := range s.recentRoots {
		thread, ok := s.threads[rootURI]
		if !ok {
			continue
		}

		summary := buildSummary(thread)

		if filterHandle != "" {
			found := false
			for _, p := range summary.Participants {
				if p == filterHandle {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		summaries = append(summaries, summary)
	}
	return summaries
}

// buildSummary computes a ThreadSummary from a Thread. Must be called with at
// least a read lock held (or on a snapshot).
func buildSummary(t *Thread) ThreadSummary {
	if len(t.Signals) == 0 {
		return ThreadSummary{RootURI: t.RootURI, UpdatedAt: t.UpdatedAt}
	}

	root := t.Signals[0]
	preview := root.Content
	if len(preview) > 120 {
		preview = preview[:120]
	}

	// Collect unique handles (up to 5), unique types, and the union of all
	// mentions across signals. Mentions matter for the feed view: a thread
	// where the user is mentioned but hasn't replied yet would otherwise be
	// invisible (no participant entry, no authorship).
	seen := make(map[string]bool)
	seenTypes := make(map[string]bool)
	seenMentions := make(map[string]bool)
	var participants []string
	var types []string
	var mentions []string

	for _, sig := range t.Signals {
		if sig.AuthorHandle != "" && !seen[sig.AuthorHandle] {
			seen[sig.AuthorHandle] = true
			if len(participants) < 5 {
				participants = append(participants, sig.AuthorHandle)
			}
		}
		if sig.Type != "" && !seenTypes[sig.Type] {
			seenTypes[sig.Type] = true
			types = append(types, sig.Type)
		}
		for _, m := range sig.Mentions {
			if m != "" && !seenMentions[m] {
				seenMentions[m] = true
				mentions = append(mentions, m)
			}
		}
	}

	return ThreadSummary{
		RootURI:      t.RootURI,
		Preview:      preview,
		AuthorHandle: root.AuthorHandle,
		SignalCount:  len(t.Signals),
		Participants: participants,
		Mentions:     mentions,
		UpdatedAt:    t.UpdatedAt,
		Types:        types,
	}
}

// GetThread returns the signals of a thread with depths computed, sorted by
// IndexedAt. Returns nil if the thread is unknown.
func (s *State) GetThread(rootURI string) []*Signal {
	s.mu.RLock()
	thread, ok := s.threads[rootURI]
	if !ok {
		s.mu.RUnlock()
		return nil
	}
	// Copy slice to avoid holding lock during depth computation.
	signals := make([]*Signal, len(thread.Signals))
	copy(signals, thread.Signals)
	s.mu.RUnlock()

	// Re-compute depths on the copy so callers always get fresh values.
	uriMap := make(map[string]*Signal, len(signals))
	for _, sig := range signals {
		uriMap[sig.URI] = sig
	}
	for _, sig := range signals {
		sig.Depth = depthFromMap(sig, uriMap)
	}

	return signals
}

// GetSignalByURI returns the indexed signal with the given AT-URI, or nil if unknown.
func (s *State) GetSignalByURI(uri string) *Signal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, thread := range s.threads {
		for _, sig := range thread.Signals {
			if sig.URI == uri {
				return sig
			}
		}
	}
	return nil
}

// GetAuthorSignals returns all indexed signals for a given handle.
func (s *State) GetAuthorSignals(handle string) []*Signal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sigs := s.byAuthor[handle]
	out := make([]*Signal, len(sigs))
	copy(out, sigs)
	return out
}

// UpsertProfile adds or updates an agent profile in the directory (keyed by DID).
func (s *State) UpsertProfile(p *AgentProfile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles[p.DID] = p
}

// RemoveProfile drops an agent profile by DID. Returns true if it was present.
func (s *State) RemoveProfile(did string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.profiles[did]; !ok {
		return false
	}
	delete(s.profiles, did)
	return true
}

// ListProfiles returns a copy of every indexed agent profile, sorted by handle.
func (s *State) ListProfiles() []*AgentProfile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*AgentProfile, 0, len(s.profiles))
	for _, p := range s.profiles {
		cp := *p
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out
}

// computeDepth computes the depth of sig within its thread by walking the
// parent chain in the already-appended thread.Signals slice.
// Must be called with s.mu held (write lock).
func computeDepth(sig *Signal, thread *Thread) int {
	uriMap := make(map[string]*Signal, len(thread.Signals))
	for _, s := range thread.Signals {
		uriMap[s.URI] = s
	}
	return depthFromMap(sig, uriMap)
}

// depthFromMap walks the parent chain in uriMap, capping at maxDepth.
func depthFromMap(sig *Signal, uriMap map[string]*Signal) int {
	depth := 0
	current := sig
	for depth < maxDepth {
		if current.ParentURI == "" {
			break
		}
		parent, ok := uriMap[current.ParentURI]
		if !ok {
			break
		}
		depth++
		current = parent
	}
	return depth
}
