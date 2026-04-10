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

	// Triage agent: looked up by handle in the registry. The bridge calls it
	// over A2A when a tagless signal arrives with no continuation context.
	triageHandle string

	// Orchestrator: the fallback agent. If triage returns empty mentions
	// (despite being told not to), the bridge routes to this handle as a
	// safety net. Also logged in the routing signal for audit.
	orchestratorHandle string

	// PDS auth (bridge's own account — used as the fallback when publishing
	// records on behalf of an agent whose credentials we don't hold)
	accessToken string
	handle      string
	password    string

	// Per-agent PDS sessions, keyed by handle. The bridge holds credentials
	// for agents we want to publish on behalf of (so the records they're
	// authored under match the agent's identity, not the bridge's). Today
	// this only contains triage; future slices will add more.
	//
	// TODO: token refresh / session renewal. Sessions currently last long
	// enough for dev runs; for longer-lived deployments we'll need to refresh
	// before the JWT expires.
	sessions map[string]agentSession
}

// agentSession is a single PDS session held by the bridge on behalf of an
// agent (or the bridge itself).
type agentSession struct {
	DID         string
	AccessToken string
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Load .env file before reading any other env vars so file values become
	// defaults that shell-set values can still override. Path defaults to
	// `.env` in the current working directory; BRIDGE_ENV_FILE overrides.
	envPath := envOrDefault("BRIDGE_ENV_FILE", ".env")
	if loaded, err := loadDotEnv(envPath); err != nil {
		logger.Warn("failed to read env file", "path", envPath, "error", err)
	} else if loaded != "" {
		logger.Info("loaded env file", "path", loaded)
	}

	relayURL := envOrDefault("RELAY_URL", "ws://localhost:2470")
	pdsURL := envOrDefault("PDS_URL", "https://pds.murmur.internal")
	bridgeHandle := envOrDefault("BRIDGE_HANDLE", "bridge.test")
	bridgePassword := envOrDefault("BRIDGE_PASSWORD", "bridge-sandbox-pass")
	discoveryAddr := envOrDefault("DISCOVERY_ADDR", ":2471")
	triageHandle := envOrDefault("TRIAGE_HANDLE", "triage.test")
	orchestratorHandle := envOrDefault("ORCHESTRATOR_HANDLE", "orchestrator.test")
	agentCredsRaw := os.Getenv("MURMUR_AGENT_CREDS")

	registry := NewAgentRegistry(logger)

	b := &Bridge{
		logger:       logger,
		relayURL:     relayURL,
		pdsURL:       pdsURL,
		registry:     registry,
		triageHandle:       triageHandle,
		orchestratorHandle: orchestratorHandle,
		handle:             bridgeHandle,
		password:     bridgePassword,
		sessions:     make(map[string]agentSession),
	}

	if err := b.authenticate(); err != nil {
		logger.Error("failed to authenticate bridge account", "error", err)
		logger.Info("continuing without PDS auth — firehose consumption will still work, but reply publishing won't")
	}

	// Open PDS sessions for any agents whose credentials the bridge holds.
	// MURMUR_AGENT_CREDS is a comma-separated list of `handle:password`
	// pairs. Each entry becomes a session in b.sessions, which the publish
	// layer uses to sign records under the agent's identity instead of the
	// bridge's. Missing/malformed entries log a warning and the bridge falls
	// back to bridge-authored signals for the affected agent.
	//
	// This is the env-var stage of the credential model: hardcoded text,
	// fine for ~10 agents. The next slice replaces it with bridge-side
	// account provisioning so credentials are derived from a single PDS
	// admin secret rather than enumerated.
	if agentCredsRaw == "" {
		logger.Warn("MURMUR_AGENT_CREDS not set — all bridge-published signals will be authored by the bridge, not by agents")
	} else {
		for _, entry := range strings.Split(agentCredsRaw, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			colon := strings.IndexByte(entry, ':')
			if colon <= 0 {
				logger.Warn("malformed MURMUR_AGENT_CREDS entry — expected handle:password", "entry", entry)
				continue
			}
			handle := strings.TrimSpace(entry[:colon])
			password := strings.TrimSpace(entry[colon+1:])
			session, err := b.createSession(handle, password)
			if err != nil {
				logger.Warn("failed to create agent PDS session — falling back to bridge authorship for this agent",
					"handle", handle,
					"error", err,
				)
				continue
			}
			b.sessions[handle] = session
			logger.Info("authenticated as agent", "handle", handle, "did", session.DID)
		}
	}

	if _, ok := b.sessions[triageHandle]; !ok {
		logger.Warn("no credentials for triage handle in MURMUR_AGENT_CREDS — routing signals will be authored by the bridge",
			"triage_handle", triageHandle,
		)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := b.backfillRegistry(ctx); err != nil {
		logger.Warn("registry backfill failed — bridge will start with empty registry and rely on live firehose events", "error", err)
	}

	if err := b.startDiscoveryServer(ctx, discoveryAddr); err != nil {
		logger.Error("failed to start discovery server", "error", err)
	}

	logger.Info("bridge starting",
		"relay", relayURL,
		"pds", pdsURL,
		"discovery", discoveryAddr,
		"triage_handle", triageHandle,
		"orchestrator_handle", orchestratorHandle,
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
			// Skip signals from any DID the bridge publishes under. This
			// dedupes the bridge's own posts (routing signals, reply signals)
			// so it doesn't re-dispatch records it just wrote. Profile events
			// are NOT skipped — agents whose credentials we hold still need
			// their profile publications processed for registry updates.
			if op.Action == "create" && !b.isOwnPublisher(evt.Repo) {
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

	// Fetch parent a2a info if this is a reply. We need it for two purposes:
	//
	// (1) Implicit chat continuation: if the user replied without explicit
	//     mentions and the parent is an agent reply, the implicit target is
	//     that agent. This is the "I replied in this thread, I'm continuing
	//     with that agent" convention — analogous to how Slack/Discord treat
	//     thread participants vs. channel posts.
	//
	// (2) Per-agent context continuity: when dispatching to an agent that
	//     produced the parent reply, pass its contextId so the agent's task
	//     store resumes the same conversation instead of starting fresh.
	var parentA2A *a2aRef
	if parentURI := parentURIOf(record); parentURI != "" {
		a, err := b.fetchParentA2A(ctx, parentURI)
		if err != nil {
			b.logger.Warn("failed to fetch parent for routing/context",
				"parent_uri", parentURI, "error", err)
		}
		parentA2A = a
	}

	// Implicit chat continuation: if no explicit mentions and the parent is
	// an agent reply, treat that agent as the sole implicit mention. The
	// per-mention dispatch loop below will then pick up the parent's
	// contextId via the matching agentDid path, giving us continuity for free.
	if len(mentions) == 0 && parentA2A != nil && parentA2A.AgentDID != "" {
		if agent := b.registry.FindByDID(parentA2A.AgentDID); agent != nil {
			b.logger.Info("implicit mention from parent agent",
				"signal_uri", uri,
				"agent", agent.Name,
				"did", parentA2A.AgentDID,
			)
			mentions = []string{parentA2A.AgentDID}
		}
	}

	// Triage: if still no mentions, the signal needs LLM-based routing.
	// Call the triage agent over A2A and use whatever it returns. Triage
	// itself is a regular A2A agent registered via its atproto profile.
	rootURI, rootCID := threadRoot(record, uri, cid)
	if len(mentions) == 0 {
		decision := b.callTriage(ctx, content)
		if decision == nil {
			return
		}

		// Publish a routing signal in the thread so the routing decision is
		// visible to anyone reading the thread later. The bridge dedupes its
		// own posts on the firehose, so this won't trigger re-dispatch.
		if b.accessToken != "" && len(decision.Mentions) > 0 {
			if err := b.publishRoutingSignal(ctx, b.triageHandle, uri, cid, rootURI, rootCID, decision.Mentions, decision.Rationale); err != nil {
				b.logger.Error("failed to publish routing signal", "error", err)
			}
		}

		if len(decision.Mentions) == 0 {
			if b.orchestratorHandle != "" {
				b.logger.Info("triage returned no mentions, falling back to orchestrator",
					"signal_uri", uri,
					"orchestrator", b.orchestratorHandle,
					"rationale", decision.Rationale,
				)
				decision.Mentions = []string{b.orchestratorHandle}
				decision.Rationale += " (bridge fallback: routed to orchestrator)"

				// Publish the routing signal for the fallback case too.
				if b.accessToken != "" {
					if err := b.publishRoutingSignal(ctx, b.triageHandle, uri, cid, rootURI, rootCID, decision.Mentions, decision.Rationale); err != nil {
						b.logger.Error("failed to publish routing signal", "error", err)
					}
				}
			} else {
				b.logger.Info("triage returned no mentions, dropping signal",
					"signal_uri", uri,
					"rationale", decision.Rationale,
				)
				return
			}
		}

		mentions = decision.Mentions
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

		// Continue the existing context only if the parent reply was from
		// THIS agent. Otherwise start a fresh task — we don't want to feed
		// agent A's contextId to agent B.
		var contextID string
		if parentA2A != nil && parentA2A.AgentDID == agent.DID {
			contextID = parentA2A.ContextID
		}

		b.logger.Info("dispatching to agent",
			"agent", agent.Name,
			"handle", agent.Handle,
			"dispatch_url", agent.A2AEndpoint,
			"signal_uri", uri,
			"context_id", contextID,
		)

		result, err := sendA2ATask(ctx, agent.A2AEndpoint, content, contextID)
		if err != nil {
			b.logger.Error("A2A dispatch failed",
				"agent", agent.Name,
				"error", err,
			)
			continue
		}

		b.logger.Info("A2A result received",
			"agent", agent.Name,
			"task_id", result.TaskID,
			"context_id", result.ContextID,
			"result", result.Text,
		)

		// Publish the result as a reply signal in the original thread,
		// carrying the a2a correlation IDs so a follow-up reply can continue.
		if b.accessToken != "" {
			ref := &a2aRef{
				AgentDID:  agent.DID,
				TaskID:    result.TaskID,
				ContextID: result.ContextID,
			}
			// Sign reply signals as the responding agent if we hold their
			// credentials; sessionFor falls back to bridge authorship for
			// agents we don't.
			if err := b.publishReplySignal(ctx, agent.Handle, uri, cid, rootURI, rootCID, agent.Name, result.Text, ref); err != nil {
				b.logger.Error("failed to publish reply signal",
					"agent", agent.Name,
					"error", err,
				)
			}
		}
	}
}

// threadRoot computes the (root URI, root CID) for replies the bridge is
// about to publish in response to an incoming signal. If the incoming signal
// is itself a reply, we carry its root forward; otherwise the incoming
// signal IS the root.
func threadRoot(record map[string]any, signalURI, signalCID string) (string, string) {
	if reply, ok := record["reply"].(map[string]any); ok {
		if root, ok := reply["root"].(map[string]any); ok {
			u, _ := root["uri"].(string)
			c, _ := root["cid"].(string)
			if u != "" && c != "" {
				return u, c
			}
		}
	}
	return signalURI, signalCID
}

// triageDecision is the JSON shape we expect back from the triage agent.
type triageDecision struct {
	Mentions  []string `json:"mentions"`
	Rationale string   `json:"rationale"`
}

// callTriage dispatches the signal content to the triage agent over A2A and
// returns its parsed routing decision. Returns nil if triage is not registered
// or the call fails — the caller should drop the signal in that case.
func (b *Bridge) callTriage(ctx context.Context, content string) *triageDecision {
	agent := b.registry.FindByHandle(b.triageHandle)
	if agent == nil {
		b.logger.Warn("no mentions and triage agent not registered, dropping signal",
			"triage_handle", b.triageHandle,
		)
		return nil
	}

	b.logger.Info("calling triage", "endpoint", agent.A2AEndpoint)
	result, err := sendA2ATask(ctx, agent.A2AEndpoint, content, "")
	if err != nil {
		b.logger.Error("triage dispatch failed", "error", err)
		return nil
	}

	var decision triageDecision
	if err := json.Unmarshal([]byte(result.Text), &decision); err != nil {
		b.logger.Warn("could not parse triage response as JSON",
			"raw", result.Text,
			"error", err,
		)
		return nil
	}

	b.logger.Info("triage decision",
		"mentions", decision.Mentions,
		"rationale", decision.Rationale,
	)
	return &decision
}

// a2aRef carries the A2A correlation IDs the bridge writes onto reply signals
// (per the community.swarm.signal lexicon's `a2a` field). On a follow-up reply
// in the same thread, the bridge reads these off the parent record to continue
// the same A2A task — this is what makes multi-turn chat work without making
// the bridge content-aware.
type a2aRef struct {
	AgentDID  string
	TaskID    string
	ContextID string
}

// fetchParentA2A reads the `a2a` field off a parent signal record. Returns
// nil (without error) if the parent has no a2a info — that's the normal case
// for human-posted parents.
func (b *Bridge) fetchParentA2A(ctx context.Context, parentURI string) (*a2aRef, error) {
	rec, err := b.getRecord(ctx, parentURI)
	if err != nil {
		return nil, err
	}

	raw, ok := rec["a2a"].(map[string]any)
	if !ok {
		return nil, nil
	}

	ref := &a2aRef{}
	if s, ok := raw["agentDid"].(string); ok {
		ref.AgentDID = s
	}
	if s, ok := raw["taskId"].(string); ok {
		ref.TaskID = s
	}
	if s, ok := raw["contextId"].(string); ok {
		ref.ContextID = s
	}

	if ref.AgentDID == "" {
		return nil, nil
	}
	return ref, nil
}

// getRecord fetches a single record by at:// URI from the PDS and returns its
// value as a generic map.
func (b *Bridge) getRecord(ctx context.Context, uri string) (map[string]any, error) {
	parts := strings.SplitN(strings.TrimPrefix(uri, "at://"), "/", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid at:// uri: %s", uri)
	}
	did, collection, rkey := parts[0], parts[1], parts[2]

	u := fmt.Sprintf("%s/xrpc/com.atproto.repo.getRecord?repo=%s&collection=%s&rkey=%s",
		b.pdsURL, url.QueryEscape(did), collection, rkey)

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
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getRecord %s: HTTP %d: %s", uri, resp.StatusCode, string(body))
	}

	var result struct {
		Value map[string]any `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode getRecord response: %w", err)
	}
	return result.Value, nil
}

// parentURIOf returns the immediate-parent URI from a signal record's reply
// field, or empty string if the signal isn't a reply.
func parentURIOf(record map[string]any) string {
	reply, ok := record["reply"].(map[string]any)
	if !ok {
		return ""
	}
	parent, ok := reply["parent"].(map[string]any)
	if !ok {
		return ""
	}
	uri, _ := parent["uri"].(string)
	return uri
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

// publishRoutingSignal writes a `routing` signal in the thread carrying the
// triage decision: which agents to dispatch to and a brief rationale. The
// bridge dispatches in parallel and dedupes its own posts on the firehose,
// so this signal exists purely for audit visibility — anyone reading the
// thread later can see how the routing decision was made.
func (b *Bridge) publishRoutingSignal(ctx context.Context, asHandle, parentURI, parentCID, rootURI, rootCID string, mentions []string, rationale string) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	content := rationale
	if content == "" {
		content = "(no rationale)"
	}

	mentionsAny := make([]any, 0, len(mentions))
	for _, m := range mentions {
		mentionsAny = append(mentionsAny, m)
	}

	record := map[string]any{
		"$type":     signalCollection,
		"type":      "routing",
		"content":   content,
		"mentions":  mentionsAny,
		"createdAt": now,
		"reply": map[string]any{
			"root": map[string]any{
				"uri": rootURI,
				"cid": rootCID,
			},
			"parent": map[string]any{
				"uri": parentURI,
				"cid": parentCID,
			},
		},
	}

	session := b.sessionFor(asHandle)

	body := map[string]any{
		"repo":       session.DID,
		"collection": signalCollection,
		"validate":   false,
		"record":     record,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal routing signal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		b.pdsURL+"/xrpc/com.atproto.repo.createRecord",
		bytes.NewReader(jsonBody),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)

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

	b.logger.Info("routing signal published",
		"uri", created.URI,
		"author_did", session.DID,
		"parent", parentURI,
		"mentions", mentions,
	)
	return nil
}

// publishReplySignal writes a `result` signal under the bridge's repo as a
// reply in an existing thread. parentURI/parentCID point at the signal we're
// directly replying to (the one being processed); rootURI/rootCID point at
// the original thread root, which may be the same as the parent if we're
// replying to a top-level signal.
func (b *Bridge) publishReplySignal(ctx context.Context, asHandle, parentURI, parentCID, rootURI, rootCID, agentName, result string, ref *a2aRef) error {
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
				"uri": parentURI,
				"cid": parentCID,
			},
		},
	}

	// Carry A2A correlation so a follow-up reply in this thread can continue
	// the same task. agentDid is required by the lexicon; the others are
	// optional but in practice the SDK always returns them on a real dispatch.
	if ref != nil && ref.AgentDID != "" {
		a2aMap := map[string]any{
			"agentDid": ref.AgentDID,
		}
		if ref.TaskID != "" {
			a2aMap["taskId"] = ref.TaskID
		}
		if ref.ContextID != "" {
			a2aMap["contextId"] = ref.ContextID
		}
		record["a2a"] = a2aMap
	}

	session := b.sessionFor(asHandle)

	body := map[string]any{
		"repo":       session.DID,
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
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)

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
		"author_did", session.DID,
		"parent", parentURI,
		"root", rootURI,
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
	session, err := b.createSession(b.handle, b.password)
	if err != nil {
		return err
	}
	b.accessToken = session.AccessToken
	b.bridgeDID = session.DID
	b.logger.Info("authenticated as bridge", "did", b.bridgeDID)
	return nil
}

// createSession opens a PDS session for an arbitrary handle/password and
// returns the resulting access token + DID. Used both for the bridge's own
// account and for any agent accounts the bridge is configured to sign on
// behalf of.
func (b *Bridge) createSession(handle, password string) (agentSession, error) {
	body := fmt.Sprintf(`{"identifier":%q,"password":%q}`, handle, password)
	resp, err := http.Post(
		b.pdsURL+"/xrpc/com.atproto.server.createSession",
		"application/json",
		bytes.NewReader([]byte(body)),
	)
	if err != nil {
		return agentSession{}, fmt.Errorf("session request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return agentSession{}, fmt.Errorf("session creation failed: HTTP %d", resp.StatusCode)
	}

	var raw struct {
		AccessJwt string `json:"accessJwt"`
		DID       string `json:"did"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return agentSession{}, fmt.Errorf("failed to decode session: %w", err)
	}

	return agentSession{DID: raw.DID, AccessToken: raw.AccessJwt}, nil
}

// isOwnPublisher reports whether the given DID belongs to the bridge or to
// any agent the bridge holds credentials for. Used by the firehose dedup to
// skip records the bridge itself authored.
func (b *Bridge) isOwnPublisher(did string) bool {
	if did == b.bridgeDID {
		return true
	}
	for _, s := range b.sessions {
		if s.DID == did {
			return true
		}
	}
	return false
}

// sessionFor returns the PDS session the bridge should use when publishing
// records on behalf of the given handle. Falls back to the bridge's own
// session if no per-agent credentials are registered for that handle. The
// fallback preserves authorship under bridge.test, which is the legacy
// behavior we're incrementally moving away from.
func (b *Bridge) sessionFor(asHandle string) agentSession {
	if asHandle != "" {
		if s, ok := b.sessions[asHandle]; ok {
			return s
		}
	}
	return agentSession{DID: b.bridgeDID, AccessToken: b.accessToken}
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
