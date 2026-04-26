package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// historyEntry is one prior turn in a thread, as carried in the
// community.swarm.threadContext DataPart. Shape is specified in
// /home/carlose/Downloads/thread-context-extension-design.md §4.3.
type historyEntry struct {
	SignalURI            string `json:"signalUri"`
	SignalCID            string `json:"signalCid"`
	HttpURL              string `json:"httpUrl"`
	AuthorDID            string `json:"authorDid"`
	AuthorHandle         string `json:"authorHandle"`
	Type                 string `json:"type"`
	Content              string `json:"content"`
	CreatedAt            string `json:"createdAt"`
	Truncated            bool   `json:"truncated"`
	OriginalContentBytes int    `json:"originalContentBytes,omitempty"`
}

// appviewSignal mirrors appview/cmd/appview/state.go:Signal for decoding
// the /api/thread response. Only the fields we need.
type appviewSignal struct {
	URI          string    `json:"uri"`
	CID          string    `json:"cid"`
	AuthorDID    string    `json:"authorDid"`
	AuthorHandle string    `json:"authorHandle"`
	Type         string    `json:"type"`
	Content      string    `json:"content"`
	CreatedAt    time.Time `json:"createdAt"`
}

// fetchThreadHistory calls the AppView's /api/thread for the given root and
// returns the thread's prior turns (oldest first), excluding excludeSignalURI
// (the signal currently being dispatched — its content is already in the
// TextPart).
//
// The AppView is the canonical atproto indexer in Murmur's stack; PDSes don't
// serve "list by reply.root" queries, which is exactly why AppViews exist. The
// bridge consults the indexer on every dispatch rather than caching thread
// state, which keeps atproto authoritative and prevents drift between "what
// the bridge thinks happened" and "what atproto says happened."
//
// Graceful degradation: any failure (AppView down, timeout, malformed JSON)
// returns (nil, nil) with a warning logged on the bridge. The caller treats
// nil as "send empty history" — the dispatch still proceeds, and behavior
// matches today's baseline where agents have no thread history to work with.
func (b *Bridge) fetchThreadHistory(ctx context.Context, rootURI, excludeSignalURI string) []historyEntry {
	if b.appviewURL == "" {
		return nil
	}

	reqURL := fmt.Sprintf("%s/api/thread?root=%s", strings.TrimRight(b.appviewURL, "/"), url.QueryEscape(rootURI))

	client := &http.Client{Timeout: 5 * time.Second}

	var signals []appviewSignal
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
		if err != nil {
			lastErr = err
			break
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("appview /api/thread HTTP %d", resp.StatusCode)
			continue
		}
		if err := json.NewDecoder(resp.Body).Decode(&signals); err != nil {
			resp.Body.Close()
			lastErr = fmt.Errorf("decode /api/thread: %w", err)
			break
		}
		resp.Body.Close()
		lastErr = nil
		break
	}
	if lastErr != nil {
		b.logger.Warn("thread history unavailable — dispatching without it",
			"root", rootURI,
			"appview", b.appviewURL,
			"error", lastErr,
		)
		return nil
	}

	entries := make([]historyEntry, 0, len(signals))
	for _, s := range signals {
		if s.URI == excludeSignalURI {
			continue
		}
		entries = append(entries, historyEntry{
			SignalURI:    s.URI,
			SignalCID:    s.CID,
			HttpURL:      b.signalHttpURL(s.URI),
			AuthorDID:    s.AuthorDID,
			AuthorHandle: s.AuthorHandle,
			Type:         s.Type,
			Content:      s.Content,
			CreatedAt:    s.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].CreatedAt < entries[j].CreatedAt
	})

	return entries
}

// signalHttpURL builds a PDS XRPC getRecord URL for a signal at-URI.
// TODO: real DID→PDS resolution. v1 assumes every DID lives on b.pdsURL,
// which holds in the current sandbox but breaks once users can migrate.
func (b *Bridge) signalHttpURL(atURI string) string {
	parts := strings.SplitN(strings.TrimPrefix(atURI, "at://"), "/", 3)
	if len(parts) != 3 {
		return ""
	}
	did, collection, rkey := parts[0], parts[1], parts[2]
	return fmt.Sprintf("%s/xrpc/com.atproto.repo.getRecord?repo=%s&collection=%s&rkey=%s",
		b.pdsURL, url.QueryEscape(did), collection, rkey)
}

// applyTruncation caps total content bytes across a history slice. If the
// total is already below maxTotalBytes, entries are returned unchanged (all
// with truncated: false). Otherwise each entry whose content exceeds
// maxPerEntryBytes is truncated structurally: the content field is shortened
// and truncated+originalContentBytes are populated. No textual truncation
// markers are ever embedded in content — that would diverge the DataPart's
// view from the underlying atproto record.
func applyTruncation(entries []historyEntry, maxTotalBytes, maxPerEntryBytes int) []historyEntry {
	total := 0
	for _, e := range entries {
		total += len(e.Content)
	}
	if total <= maxTotalBytes {
		return entries
	}
	out := make([]historyEntry, len(entries))
	for i, e := range entries {
		if len(e.Content) > maxPerEntryBytes {
			e.OriginalContentBytes = len(e.Content)
			e.Content = e.Content[:maxPerEntryBytes]
			e.Truncated = true
		}
		out[i] = e
	}
	return out
}
