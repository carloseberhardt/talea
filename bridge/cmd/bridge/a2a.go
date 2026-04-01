package main

import (
	"context"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

// sendA2ATask sends a blocking SendMessage request to an A2A agent and returns the result text.
func sendA2ATask(ctx context.Context, agentURL string, content string) (string, error) {
	endpoints := []*a2a.AgentInterface{
		a2a.NewAgentInterface(agentURL, a2a.TransportProtocolJSONRPC),
	}

	client, err := a2aclient.NewFromEndpoints(ctx, endpoints)
	if err != nil {
		return "", fmt.Errorf("failed to create A2A client: %w", err)
	}

	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(content))
	result, err := client.SendMessage(ctx, &a2a.SendMessageRequest{
		Message: msg,
	})
	if err != nil {
		return "", fmt.Errorf("A2A SendMessage failed: %w", err)
	}

	return extractResultText(result)
}

func extractResultText(result a2a.SendMessageResult) (string, error) {
	switch r := result.(type) {
	case *a2a.Message:
		return partsToText(r.Parts), nil
	case *a2a.Task:
		// Status.Message has the latest result from the agent
		if r.Status.Message != nil {
			if text := partsToText(r.Status.Message.Parts); text != "" {
				return text, nil
			}
		}
		// Fall back to history if status message is empty
		if len(r.History) > 0 {
			last := r.History[len(r.History)-1]
			if last.Role == a2a.MessageRoleAgent {
				return partsToText(last.Parts), nil
			}
		}
		return fmt.Sprintf("task %s completed (state: %s)", r.ID, r.Status.State), nil
	default:
		return "", fmt.Errorf("unexpected A2A result type: %T", result)
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
