package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

func TestResolverPDSForDID(t *testing.T) {
	mock := identity.NewMockDirectory()
	mock.Insert(identity.Identity{
		DID: syntax.DID("did:plc:known"),
		Services: map[string]identity.ServiceEndpoint{
			"atproto_pds": {Type: "AtprotoPersonalDataServer", URL: "https://pds.other.example"},
		},
	})
	// A DID with no PDS service endpoint should fall back.
	mock.Insert(identity.Identity{DID: syntax.DID("did:plc:nopds")})

	r := &Resolver{
		dir:      mock,
		fallback: "https://pds.local.example",
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx := context.Background()

	cases := []struct {
		name string
		did  string
		want string
	}{
		{"resolves to the DID's hosting PDS", "did:plc:known", "https://pds.other.example"},
		{"unknown DID falls back to local PDS", "did:plc:missing", "https://pds.local.example"},
		{"DID with no PDS endpoint falls back", "did:plc:nopds", "https://pds.local.example"},
		{"malformed DID falls back", "not-a-did", "https://pds.local.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.PDSForDID(ctx, tc.did); got != tc.want {
				t.Errorf("PDSForDID(%q) = %q, want %q", tc.did, got, tc.want)
			}
		})
	}
}
