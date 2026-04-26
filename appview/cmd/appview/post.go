package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Poster manages PDS authentication and record creation on behalf of the human
// user (MURMUR_HANDLE).
type Poster struct {
	pdsURL    string
	handle    string
	password  string
	logger    *slog.Logger

	mu          sync.Mutex
	accessToken string
	did         string
}

// NewPoster creates a Poster but does not authenticate yet. Call Authenticate
// before Post.
func NewPoster(pdsURL, handle, password string, logger *slog.Logger) *Poster {
	return &Poster{
		pdsURL:   pdsURL,
		handle:   handle,
		password: password,
		logger:   logger,
	}
}

// Authenticate logs in to the PDS and caches the access token and DID.
func (p *Poster) Authenticate(ctx context.Context) error {
	body, _ := json.Marshal(map[string]string{
		"identifier": p.handle,
		"password":   p.password,
	})
	req, err := http.NewRequestWithContext(ctx, "POST",
		p.pdsURL+"/xrpc/com.atproto.server.createSession",
		bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("auth request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth failed (%d): %s", resp.StatusCode, string(raw))
	}

	var result struct {
		AccessJwt string `json:"accessJwt"`
		DID       string `json:"did"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("parse auth response: %w", err)
	}
	if result.AccessJwt == "" || result.DID == "" {
		return fmt.Errorf("auth response missing accessJwt or did")
	}

	p.mu.Lock()
	p.accessToken = result.AccessJwt
	p.did = result.DID
	p.mu.Unlock()

	p.logger.Info("authenticated as human user", "handle", p.handle, "did", result.DID)
	return nil
}

// ReplyRef identifies the parent and root signals a reply is attached to.
// Both URIs and CIDs are required by the community.swarm.signal lexicon.
type ReplyRef struct {
	ParentURI string
	ParentCID string
	RootURI   string
	RootCID   string
}

// Me returns the authenticated handle and DID. Both are empty until
// Authenticate succeeds.
func (p *Poster) Me() (handle, did string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handle, p.did
}

// Post creates a community.swarm.signal of type "request" and returns its AT-URI and CID.
// If reply is non-nil, the signal is threaded as a reply to parent/root.
func (p *Poster) Post(ctx context.Context, content string, mentions []string, reply *ReplyRef) (string, string, error) {
	p.mu.Lock()
	token := p.accessToken
	did := p.did
	p.mu.Unlock()

	if token == "" {
		return "", "", fmt.Errorf("not authenticated — call Authenticate first")
	}

	record := map[string]any{
		"$type":     "community.swarm.signal",
		"type":      "request",
		"content":   content,
		"createdAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if len(mentions) > 0 {
		record["mentions"] = mentions
	}
	if reply != nil {
		record["reply"] = map[string]any{
			"parent": map[string]string{"uri": reply.ParentURI, "cid": reply.ParentCID},
			"root":   map[string]string{"uri": reply.RootURI, "cid": reply.RootCID},
		}
	}

	payload := map[string]any{
		"repo":       did,
		"collection": "community.swarm.signal",
		"validate":   false,
		"record":     record,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", fmt.Errorf("marshal record: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		p.pdsURL+"/xrpc/com.atproto.repo.createRecord",
		bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("build create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("create record request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("create record failed (%d): %s", resp.StatusCode, string(raw))
	}

	var result struct {
		URI string `json:"uri"`
		CID string `json:"cid"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", "", fmt.Errorf("parse create response: %w", err)
	}

	p.logger.Info("posted signal", "uri", result.URI, "handle", p.handle)
	return result.URI, result.CID, nil
}
