package main

import (
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
	UpdatedAt    time.Time `json:"updatedAt"`
	Types        []string  `json:"types"`
}

// State holds the appview's in-memory view of the swarm.
type State struct {
	mu           sync.RWMutex
	threads      map[string]*Thread   // keyed by root URI
	byAuthor     map[string][]*Signal // keyed by handle
	recentRoots  []string             // ring buffer of root URIs, newest first
	handleCache  sync.Map             // DID → handle string
	hub          *Hub
}

// NewState creates an empty State wired to the given Hub for broadcasting.
func NewState(hub *Hub) *State {
	return &State{
		threads:  make(map[string]*Thread),
		byAuthor: make(map[string][]*Signal),
		hub:      hub,
	}
}

// AddSignal indexes a new signal and broadcasts it to connected WebSocket clients.
func (s *State) AddSignal(sig *Signal) {
	s.mu.Lock()

	thread, exists := s.threads[sig.RootURI]
	if !exists {
		thread = &Thread{RootURI: sig.RootURI}
		s.threads[sig.RootURI] = thread
	}
	thread.Signals = append(thread.Signals, sig)
	thread.UpdatedAt = sig.IndexedAt

	// Compute depth while holding the lock (thread.Signals is complete up to now).
	sig.Depth = computeDepth(sig, thread)

	// byAuthor index
	if sig.AuthorHandle != "" {
		existing := s.byAuthor[sig.AuthorHandle]
		existing = append(existing, sig)
		if len(existing) > maxByAuthor {
			existing = existing[len(existing)-maxByAuthor:]
		}
		s.byAuthor[sig.AuthorHandle] = existing
	}

	// Update recentRoots ring buffer: move to front if already present, else prepend.
	s.upsertRecentRoot(sig.RootURI)

	s.mu.Unlock()

	// Broadcast outside the lock.
	s.hub.BroadcastSignal(sig)
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

	// Collect unique handles (up to 5) and unique types.
	seen := make(map[string]bool)
	seenTypes := make(map[string]bool)
	var participants []string
	var types []string

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
	}

	return ThreadSummary{
		RootURI:      t.RootURI,
		Preview:      preview,
		AuthorHandle: root.AuthorHandle,
		SignalCount:  len(t.Signals),
		Participants: participants,
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

// GetAuthorSignals returns all indexed signals for a given handle.
func (s *State) GetAuthorSignals(handle string) []*Signal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sigs := s.byAuthor[handle]
	out := make([]*Signal, len(sigs))
	copy(out, sigs)
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
