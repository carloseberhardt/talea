package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

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
// agents can call POST /publish with correct thread references.
type threadContext struct {
	SignalURI  string `json:"signalUri"`
	SignalCID  string `json:"signalCid"`
	RootURI    string `json:"rootUri"`
	RootCID    string `json:"rootCid"`
	PublishURL string `json:"publishUrl"`
}

// sendA2ATask sends a blocking SendMessage to an A2A agent. If contextID is
// non-empty, it's set on the outgoing message so the agent can group related
// tasks under the same context. If tc is non-nil, a DataPart with the thread
// context is included alongside the text so the agent can call /publish.
func sendA2ATask(ctx context.Context, agentURL, content, contextID string, tc *threadContext) (*a2aDispatchResult, error) {
	endpoints := []*a2a.AgentInterface{
		a2a.NewAgentInterface(agentURL, a2a.TransportProtocolJSONRPC),
	}

	client, err := a2aclient.NewFromEndpoints(ctx, endpoints)
	if err != nil {
		return nil, fmt.Errorf("failed to create A2A client: %w", err)
	}

	parts := []*a2a.Part{a2a.NewTextPart(content)}
	if tc != nil {
		parts = append(parts, a2a.NewDataPart(map[string]any{
			"threadContext": map[string]any{
				"signalUri":  tc.SignalURI,
				"signalCid":  tc.SignalCID,
				"rootUri":    tc.RootURI,
				"rootCid":    tc.RootCID,
				"publishUrl": tc.PublishURL,
			},
		}))
	}

	msg := a2a.NewMessage(a2a.MessageRoleUser, parts...)
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
		if text == "" && len(r.History) > 0 {
			// Fall back to the last agent message in history
			for i := len(r.History) - 1; i >= 0; i-- {
				if r.History[i].Role == a2a.MessageRoleAgent {
					text = partsToText(r.History[i].Parts)
					break
				}
			}
		}
		if text == "" {
			text = fmt.Sprintf("task %s completed (state: %s)", r.ID, r.Status.State)
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
