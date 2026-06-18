package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

func discardResolver(dir identity.Directory) *Resolver {
	return &Resolver{
		dir:      dir,
		fallback: "https://pds.local.example",
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

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

	r := discardResolver(mock)
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

// TestResolverResolvesDIDWeb exercises the real indigo did:web resolution path
// (HTTPS GET /.well-known/did.json → parse DID doc → PDS endpoint), proving the
// consumption side of federation works for did:web identities even though the
// stock PDS doesn't turnkey-provision them. indigo forces HTTPS for did:web, so
// we stand up a TLS server and redirect the dial to it.
func TestResolverResolvesDIDWeb(t *testing.T) {
	const didWeb = "did:web:example.com"
	const wantPDS = "https://pds.example.internal"
	doc := `{
		"@context": ["https://www.w3.org/ns/did/v1"],
		"id": "` + didWeb + `",
		"alsoKnownAs": ["at://agent.example.com"],
		"service": [
			{"id": "#atproto_pds", "type": "AtprotoPersonalDataServer", "serviceEndpoint": "` + wantPDS + `"}
		]
	}`

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/did.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, doc)
	}))
	defer ts.Close()

	// Trust the test server's self-signed cert and redirect every dial to it,
	// so the HTTPS-only fetch to https://example.com/.well-known/did.json lands
	// on our handler. The httptest cert covers example.com.
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", ts.Listener.Addr().String())
			},
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
	}

	base := &identity.BaseDirectory{
		HTTPClient:             client,
		SkipHandleVerification: true,
	}
	r := discardResolver(base)

	if got := r.PDSForDID(context.Background(), didWeb); got != wantPDS {
		t.Errorf("PDSForDID(%q) = %q, want %q (real did:web HTTPS resolution)", didWeb, got, wantPDS)
	}
}
