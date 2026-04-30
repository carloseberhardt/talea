package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"time"
)

const (
	backfillPageSize = 100
	backfillTimeout  = 10 * time.Second
)

// backfillSource is one repo we should walk for community.swarm.signal records.
type backfillSource struct {
	DID    string
	Handle string
}

// runBackfill walks com.atproto.repo.listRecords for every known author and
// feeds the results through state.AddSignalSilent. It runs once at startup
// in parallel with the firehose subscriber. Duplicates with firehose-indexed
// records are deduped by URI inside State.
//
// Graceful degradation: if any source fails (bridge unreachable, handle
// resolution fails, listRecords errors), the affected source is skipped with a
// warning. The AppView still functions; it just has a smaller cold-start view
// until the firehose catches up.
func runBackfill(ctx context.Context, pdsURL, discoveryURL, humanHandle string, state *State, logger *slog.Logger) {
	logger.Info("backfill starting", "pds", pdsURL, "discovery", discoveryURL, "human", humanHandle)

	sources := collectAuthors(ctx, pdsURL, discoveryURL, humanHandle, logger)
	if len(sources) == 0 {
		logger.Warn("backfill: no authors discovered, nothing to do")
		return
	}

	var allSigs []*Signal
	for _, src := range sources {
		sigs, err := listSignals(ctx, pdsURL, src.DID, src.Handle, logger)
		if err != nil {
			logger.Warn("backfill: listRecords failed", "did", src.DID, "handle", src.Handle, "error", err)
			continue
		}
		allSigs = append(allSigs, sigs...)
	}

	// Order by createdAt ascending so parents are indexed before children.
	sort.Slice(allSigs, func(i, j int) bool {
		return allSigs[i].CreatedAt.Before(allSigs[j].CreatedAt)
	})

	for _, sig := range allSigs {
		state.AddSignalSilent(sig)
	}

	logger.Info("backfill complete", "authors", len(sources), "signals", len(allSigs))
}

// collectAuthors enumerates every account whose signal records the AppView
// should backfill: the configured human handle, plus every agent in the
// bridge's discovery registry.
func collectAuthors(ctx context.Context, pdsURL, discoveryURL, humanHandle string, logger *slog.Logger) []backfillSource {
	var sources []backfillSource

	if humanHandle != "" {
		if did, err := resolveDID(ctx, pdsURL, humanHandle); err == nil && did != "" {
			sources = append(sources, backfillSource{DID: did, Handle: humanHandle})
		} else if err != nil {
			logger.Warn("backfill: failed to resolve human handle", "handle", humanHandle, "error", err)
		}
	}

	agents, err := fetchAgentRegistry(ctx, discoveryURL)
	if err != nil {
		logger.Warn("backfill: failed to fetch /agents from bridge", "error", err)
		return sources
	}
	for _, a := range agents {
		if a.DID == "" {
			continue
		}
		sources = append(sources, backfillSource{DID: a.DID, Handle: a.Handle})
	}
	return sources
}

// resolveDID resolves an atproto handle to its DID via the PDS.
func resolveDID(ctx context.Context, pdsURL, handle string) (string, error) {
	u := fmt.Sprintf("%s/xrpc/com.atproto.identity.resolveHandle?handle=%s", pdsURL, url.QueryEscape(handle))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: backfillTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resolveHandle status %d", resp.StatusCode)
	}
	var out struct {
		DID string `json:"did"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.DID, nil
}

// agentRegistryEntry mirrors the bridge's /agents wire shape.
type agentRegistryEntry struct {
	Handle      string `json:"handle"`
	DID         string `json:"did"`
	Name        string `json:"name,omitempty"`
	A2AEndpoint string `json:"a2aEndpoint"`
}

func fetchAgentRegistry(ctx context.Context, discoveryURL string) ([]agentRegistryEntry, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", discoveryURL+"/agents", nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: backfillTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/agents status %d", resp.StatusCode)
	}
	var out []agentRegistryEntry
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// listSignals walks com.atproto.repo.listRecords for one repo, paginating
// until the cursor is empty. Each record is converted to a *Signal via
// recordToSignal — the same path the firehose uses, so the indexed shape is
// identical.
func listSignals(ctx context.Context, pdsURL, did, handle string, logger *slog.Logger) ([]*Signal, error) {
	var sigs []*Signal
	cursor := ""
	client := &http.Client{Timeout: backfillTimeout}

	for {
		u := fmt.Sprintf(
			"%s/xrpc/com.atproto.repo.listRecords?repo=%s&collection=%s&limit=%d",
			pdsURL,
			url.QueryEscape(did),
			signalCollection,
			backfillPageSize,
		)
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return sigs, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return sigs, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return sigs, fmt.Errorf("listRecords status %d: %s", resp.StatusCode, string(body))
		}

		var page struct {
			Cursor  string `json:"cursor"`
			Records []struct {
				URI   string         `json:"uri"`
				CID   string         `json:"cid"`
				Value map[string]any `json:"value"`
			} `json:"records"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return sigs, err
		}

		for _, rec := range page.Records {
			sig := recordToSignal(rec.Value, rec.URI, rec.CID, did, handle)
			sigs = append(sigs, sig)
		}

		if page.Cursor == "" || len(page.Records) == 0 {
			break
		}
		cursor = page.Cursor
	}

	logger.Info("backfill: listRecords ok", "did", did, "handle", handle, "count", len(sigs))
	return sigs, nil
}
