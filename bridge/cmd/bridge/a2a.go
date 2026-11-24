package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

// threadContextExtensionURI identifies the Talea thread-context A2A extension.
// URI is the version: breaking changes mint /v2, /v3, etc.
// See /home/carlose/Downloads/thread-context-extension-design.md §3.1.
const threadContextExtensionURI = "https://swarm.community/extensions/thread-context/v1"

// threadContextDataKey is the namespaced key under which the thread-context
// payload lives inside a DataPart.
const threadContextDataKey = "community.swarm.threadContext"

// a2aDispatchResult is what sendA2ATask returns: the response text plus the
// task and context IDs the agent assigned (or echoed back). The bridge stores
// these on the published reply so a follow-up turn in the same thread can
// continue the same conversation.
type a2aDispatchResult struct {
	Text      string
	TaskID    string
	ContextID string
}

// threadContext carries atproto signal threading info in A2A messages so that
// agents can call POST /publish with correct thread references and — when
// History is populated — reconstruct prior conversation turns without relying
// on in-process state. Shape matches the v1 extension schema.
type threadContext struct {
	SignalURI     string         `json:"signalUri"`
	SignalCID     string         `json:"signalCid"`
	SignalHttpURL string         `json:"signalHttpUrl,omitempty"`
	RootURI       string         `json:"rootUri"`
	RootCID       string         `json:"rootCid"`
	RootHttpURL   string         `json:"rootHttpUrl,omitempty"`
	PublishURL    string         `json:"publishUrl"`
	RecipientDID  string         `json:"recipientDid,omitempty"`
	History       []historyEntry `json:"history,omitempty"`
}

// extensionsRoundTripper adds the X-A2A-Extensions header to every outbound
// request so extension-aware agents can detect extension activation at the
// HTTP layer (per A2A extension spec).
type extensionsRoundTripper struct {
	base       http.RoundTripper
	extensions []string
}

func (r *extensionsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for _, ext := range r.extensions {
		req.Header.Add("X-A2A-Extensions", ext)
	}
	return r.base.RoundTrip(req)
}

// sendA2ATask sends a blocking SendMessage to an A2A agent. If contextID is
// non-empty, it's set on the outgoing message so the agent can group related
// tasks under the same context. If tc is non-nil, a DataPart with the thread
// context is included alongside the text so the agent can call /publish and
// (when History is populated) reconstruct prior turns.
func sendA2ATask(ctx context.Context, agentURL, content, contextID string, tc *threadContext) (*a2aDispatchResult, error) {
	endpoints := []*a2a.AgentInterface{
		a2a.NewAgentInterface(agentURL, a2a.TransportProtocolJSONRPC),
	}

	httpClient := &http.Client{
		Transport: &extensionsRoundTripper{
			base:       http.DefaultTransport,
			extensions: []string{threadContextExtensionURI},
		},
	}

	client, err := a2aclient.NewFromEndpoints(ctx, endpoints,
		a2aclient.WithJSONRPCTransport(httpClient),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create A2A client: %w", err)
	}

	parts := []*a2a.Part{a2a.NewTextPart(content)}
	if tc != nil {
		payload := threadContextPayload(tc)
		// Emit under both the new namespaced key (the v1 extension contract)
		// and the legacy "threadContext" key so existing agents that haven't
		// adopted the extension (currently: orchestrator) continue to find
		// the publishUrl/signalUri/rootUri fields they rely on. Remove the
		// legacy key once every consumer reads the namespaced version.
		parts = append(parts, a2a.NewDataPart(map[string]any{
			threadContextDataKey: payload,
			"threadContext":      payload,
		}))
	}

	msg := a2a.NewMessage(a2a.MessageRoleUser, parts...)
	msg.Extensions = []string{threadContextExtensionURI}
	if contextID != "" {
		msg.ContextID = contextID
	}

	result, err := client.SendMessage(ctx, &a2a.SendMessageRequest{
		Message: msg,
	})
	if err != nil {
		return nil, fmt.Errorf("A2A SendMessage failed: %w", err)
	}

	return extractDispatchResult(result)
}

// threadContextPayload builds the map embedded under threadContextDataKey.
// Omits empty optional fields so consumers can cleanly distinguish
// "not present" from "empty list" for History.
func threadContextPayload(tc *threadContext) map[string]any {
	payload := map[string]any{
		"signalUri":  tc.SignalURI,
		"signalCid":  tc.SignalCID,
		"rootUri":    tc.RootURI,
		"rootCid":    tc.RootCID,
		"publishUrl": tc.PublishURL,
	}
	if tc.SignalHttpURL != "" {
		payload["signalHttpUrl"] = tc.SignalHttpURL
	}
	if tc.RootHttpURL != "" {
		payload["rootHttpUrl"] = tc.RootHttpURL
	}
	if tc.RecipientDID != "" {
		payload["recipientDid"] = tc.RecipientDID
	}
	if tc.History != nil {
		payload["history"] = tc.History
	}
	return payload
}

func extractDispatchResult(result a2a.SendMessageResult) (*a2aDispatchResult, error) {
	slog.Info("A2A response type", "type", fmt.Sprintf("%T", result))
	switch r := result.(type) {
	case *a2a.Message:
		slog.Info("A2A Message response", "task_id", r.TaskID, "context_id", r.ContextID)
		return &a2aDispatchResult{
			Text:      partsToText(r.Parts),
			TaskID:    string(r.TaskID),
			ContextID: r.ContextID,
		}, nil
	case *a2a.Task:
		text := ""
		if r.Status.Message != nil {
			text = partsToText(r.Status.Message.Parts)
		}
		if text == "" && r.Status.Message == nil && len(r.History) > 0 {
			// No status message at all — fall back to the last agent message
			// in history. If the status message exists but is empty, the agent
			// intentionally returned nothing (e.g., orchestrator dispatching
			// via /publish) — don't override with history or fallback text.
			for i := len(r.History) - 1; i >= 0; i-- {
				if r.History[i].Role == a2a.MessageRoleAgent {
					text = partsToText(r.History[i].Parts)
					break
				}
			}
		}
		return &a2aDispatchResult{
			Text:      text,
			TaskID:    string(r.ID),
			ContextID: r.ContextID,
		}, nil
	default:
		return nil, fmt.Errorf("unexpected A2A result type: %T", result)
	}
}

func partsToText(parts a2a.ContentParts) string {
	var text string
	for _, p := range parts {
		if t := p.Text(); t != "" {
			if text != "" {
				text += "\n"
			}
			text += t
		}
	}
	return text
}
