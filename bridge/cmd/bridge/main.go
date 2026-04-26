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
	"sync"
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
	logger     *slog.Logger
	relayURL   string
	pdsURL     string
	appviewURL string
	bridgeDID  string
	registry   *AgentRegistry

	// The base URL of the discovery/publish server (e.g., "http://localhost:2471").
	// Passed to agents in thread context so they can call POST /publish.
	publishBaseURL string

	// PDS auth (bridge's own account — used as the fallback when publishing
	// records on behalf of an agent whose credentials we don't hold)
	accessToken string
	handle      string
	password    string

	// Per-agent PDS sessions, keyed by handle. The bridge holds credentials
	// for agents we want to publish on behalf of (so the records they're
	// authored under match the agent's identity, not the bridge's).
	sessions   map[string]*agentSession
	sessionsMu sync.RWMutex
}

// agentSession is a single PDS session held by the bridge on behalf of an
// agent (or the bridge itself). Includes credentials so the bridge can
// re-authenticate when tokens expire.
type agentSession struct {
	DID          string
	AccessToken  string
	RefreshToken string
	Handle       string
	Password     string
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
	appviewURL := envOrDefault("APPVIEW_URL", "http://localhost:2472")
	bridgeHandle := envOrDefault("BRIDGE_HANDLE", "bridge.test")
	bridgePassword := envOrDefault("BRIDGE_PASSWORD", "bridge-sandbox-pass")
	discoveryAddr := envOrDefault("DISCOVERY_ADDR", ":2471")
	agentCredsRaw := os.Getenv("MURMUR_AGENT_CREDS")

	registry := NewAgentRegistry(logger)

	b := &Bridge{
		logger:         logger,
		relayURL:       relayURL,
		pdsURL:         pdsURL,
		appviewURL:     appviewURL,
		registry:       registry,
		handle:         bridgeHandle,
		password:       bridgePassword,
		sessions:       make(map[string]*agentSession),
		publishBaseURL: "http://localhost" + discoveryAddr,
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
		"appview", appviewURL,
		"discovery", discoveryAddr,
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

	// Extract the current signal's own a2a back-reference. Set by agents via
	// /publish when they want results from this dispatch forwarded back to them.
	// This also structurally distinguishes triage-authored routing signals
	// (no a2a field) from orchestrator-authored routing signals (carry a2a).
	var ownA2AAgentDID string
	if raw, ok := record["a2a"].(map[string]any); ok {
		if s, ok := raw["agentDid"].(string); ok {
			ownA2AAgentDID = s
		}
	}

	// Triage routing signals carry the rationale as content, not the original
	// query. Fetch the parent signal's content so we dispatch agents with the
	// actual user message. Orchestrator routing signals (published via /publish)
	// carry their intended sub-query as content and include an a2a back-ref —
	// leave those alone.
	if signalType == "routing" && ownA2AAgentDID == "" {
		if parentURI := parentURIOf(record); parentURI != "" {
			parentRec, err := b.getRecord(ctx, parentURI)
			if err == nil {
				if pc, ok := parentRec["content"].(string); ok && pc != "" {
					content = pc
				}
			}
		}
	}

	b.logger.Info("signal received",
		"uri", uri,
		"from", did,
		"type", signalType,
		"mentions", mentions,
	)

	// Fetch parent a2a info if this is a reply. Used for:
	//   (1) Implicit chat continuation — if the parent was authored by an
	//       agent and there are no explicit mentions, route to that agent.
	//   (2) Context continuity — pass the parent's contextId so the agent
	//       resumes the same conversation.
	var parentA2A *a2aRef
	var parentAuthorDID string
	if parentURI := parentURIOf(record); parentURI != "" {
		a, err := b.fetchParentA2A(ctx, parentURI)
		if err != nil {
			b.logger.Warn("failed to fetch parent for routing/context",
				"parent_uri", parentURI, "error", err)
		}
		parentA2A = a
		parentAuthorDID = authorDIDFromURI(parentURI)
	}

	// Implicit chat continuation: only for request-type signals. If no
	// explicit mentions and the parent was authored by a registered agent,
	// treat that agent as the sole implicit mention.
	//
	// We accept two signals that "the parent was an agent": (a) the parent
	// has an a2a back-reference (set on result signals when an agent
	// produced them through the bridge); (b) the parent's author DID — read
	// directly from the at:// URI — matches a registered agent. Case (b)
	// handles agent-initiated requests (e.g., threeaday's nudges), which
	// are request-type signals and therefore carry no a2a field per the
	// lexicon. Without (b), replies to those nudges fall through to triage
	// even when the agent to address is structurally obvious.
	//
	// Result and routing signals never trigger implicit continuation —
	// result signals are audit records, and routing signals always carry
	// explicit mentions.
	if len(mentions) == 0 && signalType == "request" {
		implicitDID := ""
		if parentA2A != nil && parentA2A.AgentDID != "" {
			implicitDID = parentA2A.AgentDID
		} else if parentAuthorDID != "" {
			implicitDID = parentAuthorDID
		}
		if implicitDID != "" {
			if agent := b.registry.FindByDID(implicitDID); agent != nil {
				b.logger.Info("implicit mention from parent agent",
					"signal_uri", uri,
					"agent", agent.Name,
					"did", implicitDID,
				)
				mentions = []string{implicitDID}
			}
		}
	}

	rootURI, rootCID := threadRoot(record, uri, cid)

	// Tagless requests (no mentions, no implicit continuation) are handled by
	// the firehose-native triage agent out-of-band. It watches the firehose
	// itself and publishes a routing signal. The bridge just drops the raw
	// request here; the routing signal will come back around and trigger
	// dispatch via the has-mentions path below.
	if len(mentions) == 0 && signalType == "request" {
		return
	}

	// ── Has mentions → dispatch to each agent. ─────────────────────
	// If this is a routing signal with a2a back-reference (published via /publish
	// by the orchestrator), forward the result to that agent via an explicit
	// mention on the reply signal. This uses rule #1 for delivery instead of
	// rule #2 (implicit continuation), which would loop.
	var forwardToAgent *RegisteredAgent
	if signalType == "routing" && ownA2AAgentDID != "" {
		forwardToAgent = b.registry.FindByDID(ownA2AAgentDID)
		if forwardToAgent != nil {
			b.logger.Info("result will be forwarded to a2a owner",
				"owner", forwardToAgent.Name,
				"did", ownA2AAgentDID,
			)
		}
	}

	// For triage routing signals, "skip" triage as a parent — use the thread
	// root (the original user request) so agent responses appear alongside
	// triage rather than nested under it. Triage is visible in the thread as
	// a routing annotation but doesn't act as a parent for the actual work.
	// Orchestrator routing signals (which carry an a2a back-ref) keep the
	// routing signal itself as the parent so the result reply threads under
	// the orchestrator's sub-dispatch.
	dispatchParentURI, dispatchParentCID := uri, cid
	if signalType == "routing" && ownA2AAgentDID == "" {
		dispatchParentURI, dispatchParentCID = rootURI, rootCID
	}

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
		// THIS agent. Otherwise start a fresh task.
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

		// Pull prior turns from the AppView (the canonical atproto indexer)
		// so the dispatched agent can reconstruct conversational context
		// without relying on its own in-process memory. Failures degrade
		// silently to empty history — see fetchThreadHistory for details.
		history := applyTruncation(
			b.fetchThreadHistory(ctx, rootURI, uri),
			65536, 8192,
		)

		tc := &threadContext{
			SignalURI:     dispatchParentURI,
			SignalCID:     dispatchParentCID,
			SignalHttpURL: b.signalHttpURL(dispatchParentURI),
			RootURI:       rootURI,
			RootCID:       rootCID,
			RootHttpURL:   b.signalHttpURL(rootURI),
			PublishURL:    b.publishBaseURL + "/publish",
			RecipientDID:  agent.DID,
			History:       history,
		}

		result, err := sendA2ATask(ctx, agent.A2AEndpoint, content, contextID, tc)
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

		// Publish reply signal for the audit trail. If the agent returned
		// empty text (e.g., orchestrator dispatching via /publish), skip —
		// the agent already published its own signal with correct threading.
		if b.accessToken != "" && result.Text != "" {
			ref := &a2aRef{
				AgentDID:  agent.DID,
				TaskID:    result.TaskID,
				ContextID: result.ContextID,
			}
			// If this dispatch was triggered by a routing signal with a2a
			// (e.g., orchestrator's /publish signal), include an explicit
			// mention for that agent on the reply signal so rule #1 delivers
			// the result back. Without this, rule #2 would need to fire on
			// result signals — which creates loops.
			var replyMentions []string
			if forwardToAgent != nil {
				replyMentions = []string{forwardToAgent.Handle}
			}
			if err := b.publishReplySignal(ctx, agent.Handle, dispatchParentURI, dispatchParentCID, rootURI, rootCID, agent.Name, result.Text, ref, replyMentions); err != nil {
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

// authorDIDFromURI extracts the author DID component from an at:// URI.
// Returns empty string for malformed URIs. The DID is the first path segment
// after the at:// prefix and is authoritative without a record fetch — the
// PDS guarantees that records under a given DID are owned by that DID.
func authorDIDFromURI(atURI string) string {
	parts := strings.SplitN(strings.TrimPrefix(atURI, "at://"), "/", 3)
	if len(parts) < 1 || parts[0] == "" {
		return ""
	}
	return parts[0]
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

// --- PDS record creation ---

// createRecordResult is the URI + CID of a newly-created PDS record.
type createRecordResult struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

// createSignalRecord publishes a signal record to the PDS under the given
// identity (resolved via sessionFor). Returns the committed URI and CID.
func (b *Bridge) createSignalRecord(ctx context.Context, asHandle string, record map[string]any) (*createRecordResult, error) {
	// Try up to 2 times: first attempt with current token, second after refresh.
	for attempt := range 2 {
		session := b.sessionFor(asHandle)

		body := map[string]any{
			"repo":       session.DID,
			"collection": signalCollection,
			"validate":   false,
			"record":     record,
		}

		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal signal record: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST",
			b.pdsURL+"/xrpc/com.atproto.repo.createRecord",
			bytes.NewReader(jsonBody),
		)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+session.AccessToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("PDS request failed: %w", err)
		}

		if resp.StatusCode == 401 && attempt == 0 {
			resp.Body.Close()
			// Resolve the effective handle for refresh — if asHandle didn't
			// match a session, we fell back to the bridge's own session.
			refreshHandle := session.Handle
			if refreshHandle == "" {
				refreshHandle = b.handle
			}
			b.logger.Warn("PDS returned 401, refreshing session", "handle", refreshHandle)
			if err := b.refreshOrReauth(refreshHandle, session.AccessToken); err != nil {
				b.logger.Error("session refresh failed", "handle", refreshHandle, "error", err)
				return nil, fmt.Errorf("PDS auth expired and refresh failed: %w", err)
			}
			continue // retry with refreshed token
		}

		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			respBody, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("PDS returned HTTP %d: %s", resp.StatusCode, string(respBody))
		}

		var created createRecordResult
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			return nil, fmt.Errorf("decode PDS response: %w", err)
		}

		return &created, nil
	}
	return nil, fmt.Errorf("createSignalRecord: exhausted retry attempts")
}

// buildReplyRef builds the reply threading object for a signal record.
func buildReplyRef(parentURI, parentCID, rootURI, rootCID string) map[string]any {
	return map[string]any{
		"root": map[string]any{
			"uri": rootURI,
			"cid": rootCID,
		},
		"parent": map[string]any{
			"uri": parentURI,
			"cid": parentCID,
		},
	}
}

// --- Signal publishing ---

// publishReplySignal writes a `result` signal under the bridge's repo as a
// reply in an existing thread. parentURI/parentCID point at the signal we're
// directly replying to (the one being processed); rootURI/rootCID point at
// the original thread root, which may be the same as the parent if we're
// replying to a top-level signal.
func (b *Bridge) publishReplySignal(ctx context.Context, asHandle, parentURI, parentCID, rootURI, rootCID, agentName, result string, ref *a2aRef, mentions []string) error {
	record := map[string]any{
		"$type":     signalCollection,
		"type":      "result",
		"content":   fmt.Sprintf("[%s] %s", agentName, result),
		"createdAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"reply":     buildReplyRef(parentURI, parentCID, rootURI, rootCID),
	}
	if len(mentions) > 0 {
		record["mentions"] = mentions
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

	created, err := b.createSignalRecord(ctx, asHandle, record)
	if err != nil {
		return err
	}

	b.logger.Info("reply signal published",
		"uri", created.URI,
		"author_did", b.sessionFor(asHandle).DID,
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
	// Store bridge session in the sessions map too, so refreshOrReauth can find it.
	// The bridge's own handle is stored with credentials for re-auth.
	b.sessions[b.handle] = session
	b.logger.Info("authenticated as bridge", "did", b.bridgeDID)
	return nil
}

// createSession opens a PDS session for an arbitrary handle/password and
// returns the resulting session with access + refresh tokens. Used both for
// the bridge's own account and for any agent accounts the bridge is
// configured to sign on behalf of.
func (b *Bridge) createSession(handle, password string) (*agentSession, error) {
	body := fmt.Sprintf(`{"identifier":%q,"password":%q}`, handle, password)
	resp, err := http.Post(
		b.pdsURL+"/xrpc/com.atproto.server.createSession",
		"application/json",
		bytes.NewReader([]byte(body)),
	)
	if err != nil {
		return nil, fmt.Errorf("session request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("session creation failed: HTTP %d", resp.StatusCode)
	}

	var raw struct {
		AccessJwt  string `json:"accessJwt"`
		RefreshJwt string `json:"refreshJwt"`
		DID        string `json:"did"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode session: %w", err)
	}

	return &agentSession{
		DID:          raw.DID,
		AccessToken:  raw.AccessJwt,
		RefreshToken: raw.RefreshJwt,
		Handle:       handle,
		Password:     password,
	}, nil
}

// sessionFor returns the PDS session the bridge should use when publishing
// records on behalf of the given handle. Falls back to the bridge's own
// session if no per-agent credentials are registered for that handle. The
// fallback preserves authorship under bridge.test, which is the legacy
// behavior we're incrementally moving away from.
func (b *Bridge) sessionFor(asHandle string) *agentSession {
	b.sessionsMu.RLock()
	defer b.sessionsMu.RUnlock()
	if asHandle != "" {
		if s, ok := b.sessions[asHandle]; ok {
			return s
		}
	}
	// Fall back to bridge's own session (also in the map after authenticate).
	if s, ok := b.sessions[b.handle]; ok {
		return s
	}
	return &agentSession{DID: b.bridgeDID, AccessToken: b.accessToken}
}

// refreshOrReauth refreshes the access token for a session. If the refresh
// token is also expired, falls back to a full re-authentication using stored
// credentials. The staleToken parameter is the token that triggered the 401 —
// if another goroutine already refreshed, we skip the refresh.
func (b *Bridge) refreshOrReauth(handle, staleToken string) error {
	b.sessionsMu.Lock()
	defer b.sessionsMu.Unlock()

	session, ok := b.sessions[handle]
	if !ok {
		// No session for this handle — nothing to refresh.
		return fmt.Errorf("no session for handle %q", handle)
	}

	if session.AccessToken != staleToken {
		return nil // already refreshed by another goroutine
	}

	newSession, err := b.tryRefreshOrCreate(session.RefreshToken, session.Handle, session.Password)
	if err != nil {
		return err
	}
	newSession.Handle = session.Handle
	newSession.Password = session.Password
	b.sessions[handle] = newSession

	// If this is the bridge's own session, update the top-level fields too.
	if handle == b.handle {
		b.accessToken = newSession.AccessToken
		b.bridgeDID = newSession.DID
	}

	b.logger.Info("session refreshed", "handle", handle, "did", newSession.DID)
	return nil
}

// tryRefreshOrCreate attempts to refresh using the refresh token. If that
// fails, falls back to a full createSession with credentials.
func (b *Bridge) tryRefreshOrCreate(refreshToken, handle, password string) (*agentSession, error) {
	if refreshToken != "" {
		session, err := b.refreshSession(refreshToken)
		if err == nil {
			return session, nil
		}
		b.logger.Warn("token refresh failed, re-authenticating", "handle", handle, "error", err)
	}
	return b.createSession(handle, password)
}

// refreshSession calls the PDS refreshSession endpoint to get new tokens.
func (b *Bridge) refreshSession(refreshToken string) (*agentSession, error) {
	req, err := http.NewRequest("POST",
		b.pdsURL+"/xrpc/com.atproto.server.refreshSession", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+refreshToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("refresh failed: HTTP %d", resp.StatusCode)
	}

	var raw struct {
		AccessJwt  string `json:"accessJwt"`
		RefreshJwt string `json:"refreshJwt"`
		DID        string `json:"did"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode refresh response: %w", err)
	}

	return &agentSession{
		DID:          raw.DID,
		AccessToken:  raw.AccessJwt,
		RefreshToken: raw.RefreshJwt,
	}, nil
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
