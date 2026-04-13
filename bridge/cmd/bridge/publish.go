package main

// POST /publish — the write primitive for the swarm. Any agent (or service)
// can publish a signal to atproto as its own identity by calling this endpoint.
// The bridge uses the caller's credentials (which it already holds) to sign
// the record.
//
// This is fire-and-forget: the signal is committed to the PDS and the URI/CID
// are returned. Dispatch (if the signal has mentions) happens when the signal
// arrives on the firehose — the same path every other signal takes.

import (
	"encoding/json"
	"net/http"
	"time"
)

// publishRequest is the JSON body for POST /publish.
type publishRequest struct {
	Handle   string   `json:"handle"`
	Type     string   `json:"type"`
	Content  string   `json:"content"`
	Mentions []string `json:"mentions,omitempty"`

	// Optional A2A correlation — carried on the signal so the bridge can
	// route replies back to the publishing agent via implicit continuation.
	A2A *publishA2ARef `json:"a2a,omitempty"`

	// Optional threading. If omitted, the signal starts a new thread.
	Reply *publishReplyRef `json:"reply,omitempty"`
}

type publishA2ARef struct {
	AgentDID  string `json:"agentDid"`
	TaskID    string `json:"taskId,omitempty"`
	ContextID string `json:"contextId,omitempty"`
}

type publishReplyRef struct {
	ParentURI string `json:"parentUri"`
	ParentCID string `json:"parentCid"`
	RootURI   string `json:"rootUri"`
	RootCID   string `json:"rootCid"`
}

// publishResponse is the JSON response from POST /publish.
type publishResponse struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

func (b *Bridge) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req publishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate: handle must correspond to a session the bridge holds.
	if req.Handle == "" {
		http.Error(w, "handle is required", http.StatusBadRequest)
		return
	}
	if _, ok := b.sessions[req.Handle]; !ok {
		http.Error(w, "no credentials for handle: "+req.Handle, http.StatusForbidden)
		return
	}

	if req.Content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}
	if req.Type == "" {
		req.Type = "request"
	}

	// Build the signal record.
	record := map[string]any{
		"$type":     signalCollection,
		"type":      req.Type,
		"content":   req.Content,
		"createdAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}

	if len(req.Mentions) > 0 {
		mentionsAny := make([]any, 0, len(req.Mentions))
		for _, m := range req.Mentions {
			mentionsAny = append(mentionsAny, m)
		}
		record["mentions"] = mentionsAny
	}

	if req.A2A != nil && req.A2A.AgentDID != "" {
		a2aMap := map[string]any{
			"agentDid": req.A2A.AgentDID,
		}
		if req.A2A.TaskID != "" {
			a2aMap["taskId"] = req.A2A.TaskID
		}
		if req.A2A.ContextID != "" {
			a2aMap["contextId"] = req.A2A.ContextID
		}
		record["a2a"] = a2aMap
	}

	if req.Reply != nil {
		record["reply"] = buildReplyRef(
			req.Reply.ParentURI, req.Reply.ParentCID,
			req.Reply.RootURI, req.Reply.RootCID,
		)
	}

	// Publish to PDS.
	created, err := b.createSignalRecord(r.Context(), req.Handle, record)
	if err != nil {
		b.logger.Error("publish: failed to create record", "error", err, "handle", req.Handle)
		http.Error(w, "failed to publish: "+err.Error(), http.StatusInternalServerError)
		return
	}

	b.logger.Info("publish: signal created",
		"uri", created.URI,
		"cid", created.CID,
		"handle", req.Handle,
		"type", req.Type,
		"mentions", req.Mentions,
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(publishResponse{
		URI: created.URI,
		CID: created.CID,
	})
}
