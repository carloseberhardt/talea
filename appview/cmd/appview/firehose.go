package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	comatproto "github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/atproto/atdata"
	"github.com/bluesky-social/indigo/atproto/repo"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/bluesky-social/indigo/events"
	"github.com/bluesky-social/indigo/events/schedulers/parallel"
	gorillaws "github.com/gorilla/websocket"
)

const signalCollection = "community.swarm.signal"

// runFirehose connects to the relay firehose and feeds indexed signals into
// state. It retries with a 5 s backoff on connection failures. It returns only
// when ctx is cancelled. The cursor at cursorPath persists the highest seq
// processed; restarts resume from there instead of the live tail.
func runFirehose(ctx context.Context, relayURL, pdsURL, cursorPath string, state *State, logger *slog.Logger) {
	u, err := url.Parse(relayURL)
	if err != nil {
		logger.Error("invalid relay URL", "error", err)
		return
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Path = "xrpc/com.atproto.sync.subscribeRepos"

	var lastSeq atomic.Int64
	if seq, ok := loadCursor(cursorPath); ok {
		lastSeq.Store(seq)
		logger.Info("resuming firehose from saved cursor", "path", cursorPath, "seq", seq)
	} else {
		logger.Info("no saved firehose cursor; subscribing from live tail", "path", cursorPath)
	}

	handleCache := &sync.Map{} // DID → handle string

	rsc := &events.RepoStreamCallbacks{
		RepoCommit: func(evt *comatproto.SyncSubscribeRepos_Commit) error {
			err := handleCommit(ctx, evt, pdsURL, state, handleCache, logger)
			// Advance cursor regardless of per-event success — we just need to
			// remember "we got past this seq" so reconnects don't replay from
			// before it.
			lastSeq.Store(evt.Seq)
			if writeErr := saveCursor(cursorPath, evt.Seq); writeErr != nil {
				logger.Warn("failed to persist firehose cursor", "path", cursorPath, "seq", evt.Seq, "error", writeErr)
			}
			return err
		},
	}

	var retries int
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Re-read the cursor on every connection attempt so reconnects after a
		// transient drop pick up where we left off, not where we started.
		if seq := lastSeq.Load(); seq > 0 {
			u.RawQuery = fmt.Sprintf("cursor=%d", seq)
		} else {
			u.RawQuery = ""
		}
		urlStr := u.String()
		logger.Info("connecting to relay firehose", "url", urlStr, "retries", retries)

		con, _, err := gorillaws.DefaultDialer.DialContext(ctx, urlStr, http.Header{
			"User-Agent": []string{"murmur-appview/0.1"},
		})
		if err != nil {
			logger.Warn("firehose dial failed", "error", err, "retries", retries)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			retries++
			continue
		}

		logger.Info("connected to relay firehose")
		retries = 0

		scheduler := parallel.NewScheduler(1, 100, relayURL, rsc.EventHandler)
		if err := events.HandleRepoStream(ctx, con, scheduler, nil); err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warn("firehose connection lost, reconnecting", "error", err)
		}
	}
}

func handleCommit(
	ctx context.Context,
	evt *comatproto.SyncSubscribeRepos_Commit,
	pdsURL string,
	state *State,
	handleCache *sync.Map,
	logger *slog.Logger,
) error {
	r, err := repo.VerifyCommitMessage(ctx, evt)
	if err != nil {
		logger.Warn("failed to verify commit", "did", evt.Repo, "error", err)
		return nil
	}

	for _, op := range evt.Ops {
		if op.Action != "create" {
			continue
		}

		collection, rkey, err := syntax.ParseRepoPath(op.Path)
		if err != nil {
			continue
		}
		if collection.String() != signalCollection {
			continue
		}

		recBytes, _, err := r.GetRecordBytes(ctx, collection, rkey)
		if err != nil {
			logger.Warn("failed to get record bytes", "did", evt.Repo, "path", op.Path, "error", err)
			continue
		}

		record, err := atdata.UnmarshalCBOR(recBytes)
		if err != nil {
			logger.Warn("failed to unmarshal record", "did", evt.Repo, "path", op.Path, "error", err)
			continue
		}

		uri := fmt.Sprintf("at://%s/%s/%s", evt.Repo, signalCollection, rkey.String())
		cid := ""
		if op.Cid != nil {
			cid = op.Cid.String()
		}

		handle := resolveHandle(ctx, evt.Repo, pdsURL, handleCache, logger)
		sig := recordToSignal(record, uri, cid, evt.Repo, handle)

		logger.Info("signal indexed",
			"uri", sig.URI,
			"type", sig.Type,
			"author", sig.AuthorHandle,
		)

		state.AddSignal(sig)
	}
	return nil
}

// recordToSignal converts a raw CBOR-decoded record map to a Signal.
func recordToSignal(record map[string]any, uri, cid, authorDID, authorHandle string) *Signal {
	sig := &Signal{
		URI:          uri,
		CID:          cid,
		AuthorDID:    authorDID,
		AuthorHandle: authorHandle,
		IndexedAt:    time.Now().UTC(),
	}

	sig.Type, _ = record["type"].(string)
	sig.Content, _ = record["content"].(string)

	if createdAtStr, ok := record["createdAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, createdAtStr); err == nil {
			sig.CreatedAt = t
		} else if t, err := time.Parse("2006-01-02T15:04:05.000Z", createdAtStr); err == nil {
			sig.CreatedAt = t
		}
	}
	if sig.CreatedAt.IsZero() {
		sig.CreatedAt = sig.IndexedAt
	}

	// mentions
	if rawMentions, ok := record["mentions"].([]any); ok {
		for _, m := range rawMentions {
			if s, ok := m.(string); ok {
				sig.Mentions = append(sig.Mentions, s)
			}
		}
	}

	// a2a
	if rawA2A, ok := record["a2a"].(map[string]any); ok {
		info := &A2AInfo{}
		info.AgentDID, _ = rawA2A["agentDid"].(string)
		info.TaskID, _ = rawA2A["taskId"].(string)
		info.ContextID, _ = rawA2A["contextId"].(string)
		if info.AgentDID != "" || info.TaskID != "" || info.ContextID != "" {
			sig.A2A = info
		}
	}

	// reply (parent / root)
	if rawReply, ok := record["reply"].(map[string]any); ok {
		if rawParent, ok := rawReply["parent"].(map[string]any); ok {
			sig.ParentURI, _ = rawParent["uri"].(string)
		}
		if rawRoot, ok := rawReply["root"].(map[string]any); ok {
			sig.RootURI, _ = rawRoot["uri"].(string)
		}
	}

	// If no reply root, the signal is its own root.
	if sig.RootURI == "" {
		sig.RootURI = sig.URI
	}

	// For routing signals the content is the rationale.
	if sig.Type == "routing" {
		sig.Rationale = sig.Content
	}

	return sig
}

// resolveHandle returns the handle for a DID, using a cache and falling back
// to the DID itself if the PDS lookup fails.
func resolveHandle(ctx context.Context, did, pdsURL string, cache *sync.Map, logger *slog.Logger) string {
	if v, ok := cache.Load(did); ok {
		return v.(string)
	}

	handle := fetchHandle(ctx, did, pdsURL, logger)
	cache.Store(did, handle)
	return handle
}

func fetchHandle(ctx context.Context, did, pdsURL string, logger *slog.Logger) string {
	u := fmt.Sprintf("%s/xrpc/com.atproto.repo.describeRepo?repo=%s", pdsURL, url.QueryEscape(did))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return did
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Warn("failed to resolve handle", "did", did, "error", err)
		return did
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var result struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Handle == "" {
		return did
	}
	return result.Handle
}
