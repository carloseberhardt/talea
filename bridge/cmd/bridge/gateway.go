package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// GatewayClient manages dynamic route registration with AgentGateway.
// It uses the admin API (GET/POST /config) to add A2A routes at runtime.
type GatewayClient struct {
	adminURL    string // e.g., "http://localhost:15000"
	listenerURL string // e.g., "http://localhost:3000" — where A2A dispatch goes
	logger      *slog.Logger
	mu          sync.Mutex // serialize config updates to avoid races
}

func NewGatewayClient(adminURL, listenerURL string, logger *slog.Logger) *GatewayClient {
	return &GatewayClient{
		adminURL:    adminURL,
		listenerURL: listenerURL,
		logger:      logger.With("component", "gateway"),
	}
}

// DispatchURL returns the gateway URL for dispatching to a specific agent.
func (g *GatewayClient) DispatchURL(handle string) string {
	return g.listenerURL + "/" + handle
}

// RegisterAgent adds an A2A route for the given agent handle pointing to the agent's backend.
// backendHost is the agent's full URL (e.g., "http://localhost:5006").
// AgentGateway expects host:port without scheme.
func (g *GatewayClient) RegisterAgent(handle, backendHost string) error {
	// Strip scheme — AgentGateway expects host:port only
	parsed, err := url.Parse(backendHost)
	if err == nil && parsed.Host != "" {
		backendHost = parsed.Host
	}

	// Translate localhost to host.docker.internal for Docker networking.
	// The agent runs on the host, but the gateway runs in a container.
	backendHost = strings.Replace(backendHost, "localhost", "host.docker.internal", 1)
	backendHost = strings.Replace(backendHost, "127.0.0.1", "host.docker.internal", 1)

	g.mu.Lock()
	defer g.mu.Unlock()

	config, err := g.getConfig()
	if err != nil {
		return fmt.Errorf("failed to get gateway config: %w", err)
	}

	route := map[string]any{
		"name": handle,
		"matches": []any{
			map[string]any{
				"path": map[string]any{
					"pathPrefix": "/" + handle,
				},
			},
		},
		"backends": []any{
			map[string]any{"host": backendHost},
		},
		"policies": map[string]any{
			"a2a": map[string]any{},
			"urlRewrite": map[string]any{
				"path": map[string]any{
					"prefix": "/",
				},
			},
		},
	}

	updated := g.upsertRoute(config, handle, route)

	if err := g.postConfig(updated); err != nil {
		return err
	}

	g.logger.Info("gateway route registered",
		"handle", handle,
		"backend", backendHost,
		"dispatch_url", g.DispatchURL(handle),
	)
	return nil
}

// upsertRoute replaces an existing route by name or appends a new one.
func (g *GatewayClient) upsertRoute(config map[string]any, name string, route map[string]any) map[string]any {
	binds, _ := config["binds"].([]any)
	if len(binds) == 0 {
		config["binds"] = []any{
			map[string]any{
				"port": 3000,
				"listeners": []any{
					map[string]any{
						"name":   "a2a",
						"routes": []any{route},
					},
				},
			},
		}
		return config
	}

	bind, _ := binds[0].(map[string]any)
	listeners, _ := bind["listeners"].([]any)
	if len(listeners) == 0 {
		bind["listeners"] = []any{
			map[string]any{
				"name":   "a2a",
				"routes": []any{route},
			},
		}
		return config
	}

	listener, _ := listeners[0].(map[string]any)
	routes, _ := listener["routes"].([]any)

	// Replace existing or append
	found := false
	for i, r := range routes {
		rm, ok := r.(map[string]any)
		if ok {
			if n, _ := rm["name"].(string); n == name {
				routes[i] = route
				found = true
				break
			}
		}
	}
	if !found {
		routes = append(routes, route)
	}

	listener["routes"] = routes
	return config
}

func (g *GatewayClient) getConfig() (map[string]any, error) {
	resp, err := http.Get(g.adminURL + "/config")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET /config returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var config map[string]any
	if err := json.Unmarshal(body, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	return config, nil
}

func (g *GatewayClient) postConfig(config map[string]any) error {
	body, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	resp, err := http.Post(g.adminURL+"/config", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("POST /config failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST /config returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
