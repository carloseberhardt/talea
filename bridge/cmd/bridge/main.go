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

	// Seed registry with the delivery agent for testing.
	// In the future, this will be populated dynamically from agent.profile records on the firehose.
	if endpoint := os.Getenv("SEED_AGENT_ENDPOINT"); endpoint != "" {
		tags := strings.Split(envOrDefault("SEED_AGENT_TAGS", "delivery,tracking,packages"), ",")
		registry.Register(RegisteredAgent{
			Name:        envOrDefault("SEED_AGENT_NAME", "delivery-agent"),
			A2AEndpoint: endpoint,
			Tags:        tags,
		})
	}

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

	logger.Info("bridge starting",
		"relay", relayURL,
		"pds", pdsURL,
		"did", b.bridgeDID,
		"registered_agents", len(registry.agents),
	)

	if err := b.consumeFirehose(ctx); err != nil && ctx.Err() == nil {
		logger.Error("firehose consumer exited", "error", err)
		os.Exit(1)
	}

	logger.Info("bridge stopped")
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

	var capabilities []string
	if caps, ok := record["capabilities"].([]any); ok {
		for _, c := range caps {
			if s, ok := c.(string); ok {
				capabilities = append(capabilities, s)
			}
		}
	}

	uri := fmt.Sprintf("at://%s/%s/%s", did, signalCollection, rkey)

	b.logger.Info("signal received",
		"uri", uri,
		"from", did,
		"type", signalType,
		"content", content,
		"capabilities", capabilities,
	)

	if len(capabilities) == 0 {
		b.logger.Info("signal has no capability tags — skipping (slow path not implemented)")
		return
	}

	// Find matching agents
	agents := b.registry.FindByCapability(capabilities)
	if len(agents) == 0 {
		b.logger.Info("no agents match capabilities", "capabilities", capabilities)
		return
	}

	// Dispatch to all matching agents (v0 strategy: let them all respond)
	for _, agent := range agents {
		b.logger.Info("dispatching to agent",
			"agent", agent.Name,
			"endpoint", agent.A2AEndpoint,
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

// --- Agent profile discovery ---

func (b *Bridge) handleAgentProfile(ctx context.Context, did string, record map[string]any) {
	name, _ := record["name"].(string)
	endpoint, _ := record["a2aEndpoint"].(string)

	if endpoint == "" {
		return
	}

	var tags []string
	if caps, ok := record["capabilities"].([]any); ok {
		for _, c := range caps {
			if capMap, ok := c.(map[string]any); ok {
				if id, ok := capMap["id"].(string); ok {
					tags = append(tags, id)
				}
			}
		}
	}

	b.logger.Info("discovered agent profile",
		"did", did,
		"name", name,
		"endpoint", endpoint,
		"tags", tags,
	)

	b.registry.Register(RegisteredAgent{
		DID:         did,
		Name:        name,
		A2AEndpoint: endpoint,
		Tags:        tags,
	})
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
