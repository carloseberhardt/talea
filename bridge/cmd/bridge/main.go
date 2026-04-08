package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	comatproto "github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/atproto/atdata"
	"github.com/bluesky-social/indigo/atproto/repo"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/bluesky-social/indigo/events"
	"github.com/bluesky-social/indigo/events/schedulers/parallel"
	"github.com/gorilla/websocket"
)

const (
	signalCollection  = "community.swarm.signal"
	profileCollection = "community.swarm.agent.profile"
)

type Bridge struct {
	logger   *slog.Logger
	relayURL string
	pdsURL   string
	bridgeDID string
	registry *AgentRegistry

	// PDS auth
	accessToken string
	handle      string
	password    string
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	relayURL := envOrDefault("RELAY_URL", "ws://localhost:2470")
	pdsURL := envOrDefault("PDS_URL", "https://pds.murmur.internal")
	bridgeHandle := envOrDefault("BRIDGE_HANDLE", "bridge.test")
	bridgePassword := envOrDefault("BRIDGE_PASSWORD", "bridge-sandbox-pass")

	registry := NewAgentRegistry(logger)

	b := &Bridge{
		logger:   logger,
		relayURL: relayURL,
		pdsURL:   pdsURL,
		registry: registry,
		handle:   bridgeHandle,
		password: bridgePassword,
	}

	if err := b.authenticate(); err != nil {
		logger.Error("failed to authenticate bridge account", "error", err)
		logger.Info("continuing without PDS auth — firehose consumption will still work, but reply publishing won't")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := b.backfillRegistry(ctx); err != nil {
		logger.Warn("registry backfill failed — bridge will start with empty registry and rely on live firehose events", "error", err)
	}

	logger.Info("bridge starting",
		"relay", relayURL,
		"pds", pdsURL,
		"did", b.bridgeDID,
		"registered_agents", len(registry.All()),
	)

	if err := b.consumeFirehose(ctx); err != nil && ctx.Err() == nil {
		logger.Error("firehose consumer exited", "error", err)
		os.Exit(1)
	}

	logger.Info("bridge stopped")
}

// --- Startup registry backfill ---

// backfillRegistry queries the PDS for all existing agent profiles and seeds
// the registry with them before the firehose subscription begins. This makes
// bridge restarts safe: agents that registered while the bridge was down
// don't need to re-publish their profiles to be discovered.
//
// The PDS is the source of truth for repo state; the firehose is for changes.
// We treat backfill as authoritative for "what exists right now" and let the
// firehose take over for live updates from the moment we connect.
func (b *Bridge) backfillRegistry(ctx context.Context) error {
	dids, err := b.listAllRepos(ctx)
	if err != nil {
		return fmt.Errorf("listRepos failed: %w", err)
	}

	b.logger.Info("backfilling registry from PDS", "repos", len(dids))

	var found int
	for _, did := range dids {
		if did == b.bridgeDID {
			continue
		}
		record, err := b.fetchAgentProfile(ctx, did)
		if err != nil {
			b.logger.Warn("failed to fetch agent profile during backfill", "did", did, "error", err)
			continue
		}
		if record == nil {
			continue
		}
		b.handleAgentProfile(ctx, did, record)
		found++
	}

	b.logger.Info("registry backfill complete", "agents_loaded", found)
	return nil
}

// listAllRepos enumerates every DID hosted on the PDS, paginating via cursor.
func (b *Bridge) listAllRepos(ctx context.Context) ([]string, error) {
	var dids []string
	cursor := ""
	for {
		u := fmt.Sprintf("%s/xrpc/com.atproto.sync.listRepos?limit=500", b.pdsURL)
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}

		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
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
			return nil, fmt.Errorf("decode listRepos response: %w", err)
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

// fetchAgentProfile returns the community.swarm.agent.profile record for a DID,
// or nil if the repo has no profile of that collection.
func (b *Bridge) fetchAgentProfile(ctx context.Context, did string) (map[string]any, error) {
	u := fmt.Sprintf("%s/xrpc/com.atproto.repo.listRecords?repo=%s&collection=%s&limit=1",
		b.pdsURL, url.QueryEscape(did), profileCollection)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		// 400 here typically means "no records of that collection in this repo" — not an error.
		return nil, nil
	}

	var result struct {
		Records []struct {
			Value map[string]any `json:"value"`
		} `json:"records"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode listRecords response: %w", err)
	}

	if len(result.Records) == 0 {
		return nil, nil
	}
	return result.Records[0].Value, nil
}

// --- Firehose consumption ---

func (b *Bridge) consumeFirehose(ctx context.Context) error {
	u, err := url.Parse(b.relayURL)
	if err != nil {
		return fmt.Errorf("invalid relay URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Path = "xrpc/com.atproto.sync.subscribeRepos"

	rsc := &events.RepoStreamCallbacks{
		RepoCommit: func(evt *comatproto.SyncSubscribeRepos_Commit) error {
			return b.handleCommit(ctx, evt)
		},
	}

	var retries int
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		urlStr := u.String()
		b.logger.Info("connecting to firehose", "url", urlStr, "retries", retries)

		con, _, err := websocket.DefaultDialer.DialContext(ctx, urlStr, http.Header{
			"User-Agent": []string{"murmur-bridge/0.1"},
		})
		if err != nil {
			b.logger.Warn("firehose dial failed", "error", err, "retries", retries)
			time.Sleep(backoff(retries, 10))
			retries++
			continue
		}

		b.logger.Info("connected to firehose")
		retries = 0

		scheduler := parallel.NewScheduler(1, 100, b.relayURL, rsc.EventHandler)
		if err := events.HandleRepoStream(ctx, con, scheduler, nil); err != nil {
			b.logger.Warn("firehose connection lost", "error", err)
		}
	}
}

func (b *Bridge) handleCommit(ctx context.Context, evt *comatproto.SyncSubscribeRepos_Commit) error {
	if evt.Repo == b.bridgeDID {
		return nil
	}

	r, err := repo.VerifyCommitMessage(ctx, evt)
	if err != nil {
		b.logger.Warn("failed to verify commit", "did", evt.Repo, "error", err)
		return nil
	}

	for _, op := range evt.Ops {
		collection, rkey, err := syntax.ParseRepoPath(op.Path)
		if err != nil {
			continue
		}

		colStr := collection.String()
		if colStr != signalCollection && colStr != profileCollection {
			continue
		}

		// Deletes carry no record bytes — handle them before fetching.
		// Today only profile deletes are meaningful (drop the agent from
		// the registry); signal deletes are ignored.
		if op.Action == "delete" {
			if colStr == profileCollection {
				b.handleAgentProfileDelete(evt.Repo)
			}
			continue
		}

		if op.Action != "create" && op.Action != "update" {
			continue
		}

		recBytes, _, err := r.GetRecordBytes(ctx, collection, rkey)
		if err != nil {
			b.logger.Warn("failed to get record bytes", "did", evt.Repo, "path", op.Path, "error", err)
			continue
		}

		record, err := atdata.UnmarshalCBOR(recBytes)
		if err != nil {
			b.logger.Warn("failed to unmarshal record", "did", evt.Repo, "path", op.Path, "error", err)
			continue
		}

		switch colStr {
		case signalCollection:
			if op.Action == "create" {
				b.handleSignal(ctx, evt.Repo, rkey.String(), op.Cid.String(), record)
			}
		case profileCollection:
			b.handleAgentProfile(ctx, evt.Repo, record)
		}
	}

	return nil
}

// --- Signal handling and A2A dispatch ---

func (b *Bridge) handleSignal(ctx context.Context, did string, rkey string, cid string, record map[string]any) {
	signalType, _ := record["type"].(string)
	content, _ := record["content"].(string)

	mentions := extractMentions(record)

	uri := fmt.Sprintf("at://%s/%s/%s", did, signalCollection, rkey)

	b.logger.Info("signal received",
		"uri", uri,
		"from", did,
		"type", signalType,
		"mentions", mentions,
	)

	if len(mentions) == 0 {
		// Tagless signals get picked up by swarm agents (e.g., triage), not the bridge.
		return
	}

	// Resolve mentions to registered agents and dispatch.
	for _, mention := range mentions {
		agent := b.resolveMention(mention)
		if agent == nil {
			b.logger.Warn("mention does not resolve to a registered agent",
				"mention", mention,
				"signal_uri", uri,
			)
			continue
		}

		b.logger.Info("dispatching to agent",
			"agent", agent.Name,
			"handle", agent.Handle,
			"dispatch_url", agent.A2AEndpoint,
			"signal_uri", uri,
		)

		result, err := sendA2ATask(ctx, agent.A2AEndpoint, content)
		if err != nil {
			b.logger.Error("A2A dispatch failed",
				"agent", agent.Name,
				"error", err,
			)
			continue
		}

		b.logger.Info("A2A result received",
			"agent", agent.Name,
			"result", result,
		)

		// Publish the result as a reply signal in the original thread
		if b.accessToken != "" {
			if err := b.publishReplySignal(ctx, uri, cid, agent.Name, result); err != nil {
				b.logger.Error("failed to publish reply signal",
					"agent", agent.Name,
					"error", err,
				)
			}
		}
	}
}

// extractMentions reads the structured `mentions` field off a signal record.
// Mentions are handles or DIDs; the bridge does not parse mentions out of
// content text — that's the publisher's job.
func extractMentions(record map[string]any) []string {
	raw, ok := record["mentions"].([]any)
	if !ok {
		return nil
	}
	var mentions []string
	for _, m := range raw {
		if s, ok := m.(string); ok && s != "" {
			mentions = append(mentions, s)
		}
	}
	return mentions
}

// resolveMention looks up a mention (handle or DID) in the registry. Returns
// nil if no registered agent matches.
func (b *Bridge) resolveMention(mention string) *RegisteredAgent {
	if strings.HasPrefix(mention, "did:") {
		return b.registry.FindByDID(mention)
	}
	return b.registry.FindByHandle(mention)
}

// --- Agent profile discovery ---

func (b *Bridge) handleAgentProfile(ctx context.Context, did string, record map[string]any) {
	name, _ := record["name"].(string)
	endpoint, _ := record["a2aEndpoint"].(string)

	if endpoint == "" {
		return
	}

	b.logger.Info("discovered agent profile",
		"did", did,
		"name", name,
		"endpoint", endpoint,
	)

	// We deliberately do NOT gate registration on a liveness check. An agent
	// that's briefly down at bridge-restart time used to get permanently
	// dropped from the registry, which is worse than the protection the
	// liveness check was supposed to provide. Dispatch already handles
	// unreachable endpoints visibly (logs an error, moves on), so the
	// registration-time check was redundant and harmful.

	handle := b.resolveHandle(ctx, did)

	b.registry.Upsert(RegisteredAgent{
		DID:             did,
		Name:            name,
		Handle:          handle,
		A2AEndpoint:     endpoint,
		ManagedByBridge: false, // self-published; bridge-minted profiles will set true
	})
}

// handleAgentProfileDelete removes an agent from the registry when its profile
// record is deleted. The agent profile is keyed by rkey "self" so the DID is
// enough to identify the entry.
func (b *Bridge) handleAgentProfileDelete(did string) {
	if removed := b.registry.Remove(did); !removed {
		b.logger.Debug("profile delete for unknown agent", "did", did)
	}
}

// --- PDS interaction: publish reply signals ---

func (b *Bridge) publishReplySignal(ctx context.Context, rootURI string, rootCID string, agentName string, result string) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	record := map[string]any{
		"$type":     signalCollection,
		"type":      "result",
		"content":   fmt.Sprintf("[%s] %s", agentName, result),
		"createdAt": now,
		"reply": map[string]any{
			"root": map[string]any{
				"uri": rootURI,
				"cid": rootCID,
			},
			"parent": map[string]any{
				"uri": rootURI,
				"cid": rootCID,
			},
		},
	}

	body := map[string]any{
		"repo":       b.bridgeDID,
		"collection": signalCollection,
		"validate":   false,
		"record":     record,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal record: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		b.pdsURL+"/xrpc/com.atproto.repo.createRecord",
		bytes.NewReader(jsonBody),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("PDS request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PDS returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var created struct {
		URI string `json:"uri"`
	}
	json.NewDecoder(resp.Body).Decode(&created)

	b.logger.Info("reply signal published",
		"uri", created.URI,
		"in_reply_to", rootURI,
		"agent", agentName,
	)
	return nil
}

// --- Handle resolution ---

// resolveHandle resolves a DID to its atproto handle via the PDS.
func (b *Bridge) resolveHandle(ctx context.Context, did string) string {
	url := fmt.Sprintf("%s/xrpc/com.atproto.repo.describeRepo?repo=%s", b.pdsURL, did)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		b.logger.Warn("failed to build handle resolution request", "did", did, "error", err)
		return ""
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.logger.Warn("failed to resolve handle", "did", did, "error", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b.logger.Warn("handle resolution failed", "did", did, "status", resp.StatusCode)
		return ""
	}

	var result struct {
		Handle string `json:"handle"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		b.logger.Warn("failed to decode handle resolution", "did", did, "error", err)
		return ""
	}

	return result.Handle
}

// --- Auth ---

func (b *Bridge) authenticate() error {
	body := fmt.Sprintf(`{"identifier":%q,"password":%q}`, b.handle, b.password)
	resp, err := http.Post(
		b.pdsURL+"/xrpc/com.atproto.server.createSession",
		"application/json",
		bytes.NewReader([]byte(body)),
	)
	if err != nil {
		return fmt.Errorf("session request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("session creation failed: HTTP %d", resp.StatusCode)
	}

	var session struct {
		AccessJwt string `json:"accessJwt"`
		DID       string `json:"did"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return fmt.Errorf("failed to decode session: %w", err)
	}

	b.accessToken = session.AccessJwt
	b.bridgeDID = session.DID
	b.logger.Info("authenticated as bridge", "did", b.bridgeDID)
	return nil
}

// --- Utilities ---

func backoff(retries, maxRetries int) time.Duration {
	d := time.Duration(1<<min(retries, maxRetries)) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
