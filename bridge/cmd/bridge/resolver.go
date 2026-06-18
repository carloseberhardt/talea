package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

// Resolver maps an atproto DID to the PDS that hosts it, method-agnostically:
// did:plc via the configured PLC directory, did:web via HTTPS to the domain.
// This is what lets the bridge fetch records and resolve handles for any
// identity — on any PDS — instead of assuming everything lives on b.pdsURL. In
// the single-instance sandbox every DID resolves to the local PDS, so behavior
// is unchanged; under federation each DID resolves to wherever it actually
// lives. Resolution failures fall back to the local PDS, so a PLC hiccup
// degrades to today's behavior rather than breaking reads.
type Resolver struct {
	dir      identity.Directory
	fallback string
	logger   *slog.Logger
}

// NewResolver builds a cached, method-agnostic DID resolver. plcURL points at
// the PLC directory used for did:plc resolution; fallbackPDS is returned when
// resolution fails or the DID declares no PDS endpoint.
func NewResolver(plcURL, fallbackPDS string, logger *slog.Logger) *Resolver {
	base := &identity.BaseDirectory{
		PLCURL:     plcURL,
		HTTPClient: http.Client{Timeout: 5 * time.Second},
		// We use this only for DID→PDS resolution and read handle strings from
		// the PDS directly, so skip the bidirectional handle verification — it
		// needs DNS / .well-known that the sandbox's .test handles don't have.
		SkipHandleVerification: true,
		UserAgent:              "murmur-bridge/0.1",
	}
	dir := identity.NewCacheDirectory(base, 10_000, time.Hour, 5*time.Minute, 5*time.Minute)
	return &Resolver{dir: dir, fallback: fallbackPDS, logger: logger}
}

// PDSForDID returns the PDS service endpoint hosting did, or the fallback PDS if
// the DID can't be resolved or declares no PDS endpoint.
func (r *Resolver) PDSForDID(ctx context.Context, did string) string {
	parsed, err := syntax.ParseDID(did)
	if err != nil {
		return r.fallback
	}
	id, err := r.dir.LookupDID(ctx, parsed)
	if err != nil {
		r.logger.Debug("DID resolution failed; using fallback PDS", "did", did, "error", err)
		return r.fallback
	}
	if pds := id.PDSEndpoint(); pds != "" {
		return pds
	}
	return r.fallback
}
