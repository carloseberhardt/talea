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
func runBackfill(ctx context.Context, pdsURL, humanHandle string, state *State, logger *slog.Logger) {
	logger.Info("backfill starting", "pds", pdsURL, "human", humanHandle)

	sources := collectAuthors(ctx, pdsURL, humanHandle, state, logger)
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

	logger.Info("backfill complete", "authors", len(sources), "profiles", len(state.ListProfiles()), "signals", len(allSigs))
}

// collectAuthors enumerates every repo on the PDS, seeds the agent directory
// from each repo's community.swarm.agent.profile record, and returns the set of
// authors (agents + the configured human) whose signal records should be
// backfilled. The AppView discovers agents itself — it no longer asks the
// bridge — so the indexer owns the directory.
//
// listRepos is single-PDS; in a federated deployment, live discovery comes from
// the relay firehose (which aggregates every PDS) and this backfill is just a
// cold-start convenience for the local PDS.
func collectAuthors(ctx context.Context, pdsURL, humanHandle string, state *State, logger *slog.Logger) []backfillSource {
	seen := make(map[string]bool)
	var sources []backfillSource

	add := func(did, handle string) {
		if did == "" || seen[did] {
			return
		}
		seen[did] = true
		sources = append(sources, backfillSource{DID: did, Handle: handle})
	}

	if humanHandle != "" {
		if did, err := resolveDID(ctx, pdsURL, humanHandle); err == nil && did != "" {
			add(did, humanHandle)
		} else if err != nil {
			logger.Warn("backfill: failed to resolve human handle", "handle", humanHandle, "error", err)
		}
	}

	dids, err := listAllRepos(ctx, pdsURL, logger)
	if err != nil {
		logger.Warn("backfill: listRepos failed — directory will populate from the firehose only", "error", err)
		return sources
	}
	for _, did := range dids {
		rec, err := fetchProfileRecord(ctx, pdsURL, did)
		if err != nil {
			logger.Warn("backfill: failed to fetch agent profile", "did", did, "error", err)
			continue
		}
		if rec == nil {
			continue // repo has no profile — not an agent
		}
		handle := fetchHandle(ctx, did, pdsURL, logger)
		if prof := recordToProfile(rec, did, handle); prof != nil {
			state.UpsertProfile(prof)
			add(did, handle)
		}
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

// listAllRepos enumerates every DID hosted on the PDS, paginating via cursor.
// This is the same enumeration the bridge does for its registry; here it drives
// directory + signal backfill so the AppView doesn't depend on the bridge.
func listAllRepos(ctx context.Context, pdsURL string, logger *slog.Logger) ([]string, error) {
	var dids []string
	cursor := ""
	client := &http.Client{Timeout: backfillTimeout}
	for {
		u := fmt.Sprintf("%s/xrpc/com.atproto.sync.listRepos?limit=500", pdsURL)
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return dids, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return dids, err
		}
		var page struct {
			Cursor string `json:"cursor"`
			Repos  []struct {
				DID string `json:"did"`
			} `json:"repos"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return dids, fmt.Errorf("decode listRepos: %w", err)
		}
		for _, r := range page.Repos {
			dids = append(dids, r.DID)
		}
		if page.Cursor == "" || len(page.Repos) == 0 {
			break
		}
		cursor = page.Cursor
	}
	return dids, nil
}

// fetchProfileRecord returns the community.swarm.agent.profile record value for
// a DID, or nil if the repo has no profile of that collection.
func fetchProfileRecord(ctx context.Context, pdsURL, did string) (map[string]any, error) {
	u := fmt.Sprintf("%s/xrpc/com.atproto.repo.listRecords?repo=%s&collection=%s&limit=1",
		pdsURL, url.QueryEscape(did), profileCollection)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
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
		// Typically "no records of that collection in this repo" — not an error.
		return nil, nil
	}
	var result struct {
		Records []struct {
			Value map[string]any `json:"value"`
		} `json:"records"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode listRecords: %w", err)
	}
	if len(result.Records) == 0 {
		return nil, nil
	}
	return result.Records[0].Value, nil
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
