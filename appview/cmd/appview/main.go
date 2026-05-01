package main

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
)

//go:embed static
var staticFiles embed.FS

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Load .env file; shell-set vars take precedence.
	envPath := envOrDefault("BRIDGE_ENV_FILE", ".env")
	if loaded, err := loadDotEnv(envPath); err != nil {
		logger.Warn("failed to read env file", "path", envPath, "error", err)
	} else if loaded != "" {
		logger.Info("loaded env file", "path", loaded)
	}

	relayURL := envOrDefault("RELAY_URL", "ws://localhost:2470")
	pdsURL := envOrDefault("PDS_URL", "https://pds.murmur.internal")
	addr := envOrDefault("APPVIEW_ADDR", ":2472")
	discoveryURL := envOrDefault("BRIDGE_DISCOVERY_URL", "http://localhost:2471")
	murmurHandle := envOrDefault("MURMUR_HANDLE", "carlos.test")
	murmurPassword := envOrDefault("MURMUR_PASSWORD", "carlos-sandbox-pass")
	cursorPath := envOrDefault("APPVIEW_CURSOR_PATH", "appview.cursor")

	logger.Info("appview starting",
		"addr", addr,
		"relay", relayURL,
		"pds", pdsURL,
		"discovery", discoveryURL,
		"handle", murmurHandle,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	hub := NewHub(logger)
	go hub.Run()

	state := NewState(hub)

	poster := NewPoster(pdsURL, murmurHandle, murmurPassword, logger)
	if err := poster.Authenticate(ctx); err != nil {
		logger.Warn("PDS authentication failed — posting will be unavailable", "error", err)
	}

	mux := http.NewServeMux()
	registerHandlers(mux, state, hub, poster, discoveryURL, logger)

	// Serve embedded static files at / (strip the "static/" prefix).
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		logger.Error("failed to create static sub-fs", "error", err)
		os.Exit(1)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	srv := &http.Server{Addr: addr, Handler: mux}

	// Start the firehose subscriber in the background.
	go runFirehose(ctx, relayURL, pdsURL, cursorPath, state, logger)

	// Walk every known repo's community.swarm.signal records once at startup
	// to seed the in-memory index. Runs in parallel with the firehose; State
	// dedupes by URI.
	go runBackfill(ctx, pdsURL, discoveryURL, murmurHandle, state, logger)

	// Shut down HTTP server when context is cancelled.
	go func() {
		<-ctx.Done()
		logger.Info("shutting down")
		srv.Shutdown(context.Background())
	}()

	logger.Info("appview listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("http server error", "error", err)
		os.Exit(1)
	}

	logger.Info("appview stopped")
}
