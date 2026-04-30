// Murmur CLI — post and read swarm signals against a local PDS.
//
// Auth via env vars (with sandbox-friendly defaults):
//   MURMUR_PDS_URL    default: https://pds.murmur.internal
//   MURMUR_HANDLE     default: carlos.test
//   MURMUR_PASSWORD   default: carlos-sandbox-pass
//
// Subcommands:
//   murmur post "<text>" [--mention handle] [--type request|observation|...]
//   murmur thread <signal-uri>
//   murmur agents
//   murmur whoami
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	signalCollection  = "community.swarm.signal"
	profileCollection = "community.swarm.agent.profile"
)

// ─── Config and session ───────────────────────────────────────

type config struct {
	PDSURL   string
	Handle   string
	Password string
}

func loadConfig() config {
	return config{
		PDSURL:   envOr("MURMUR_PDS_URL", "https://pds.murmur.internal"),
		Handle:   envOr("MURMUR_HANDLE", "carlos.test"),
		Password: envOr("MURMUR_PASSWORD", "carlos-sandbox-pass"),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type session struct {
	AccessJwt string `json:"accessJwt"`
	DID       string `json:"did"`
	Handle    string `json:"handle"`
}

func authenticate(cfg config) (*session, error) {
	body, _ := json.Marshal(map[string]string{
		"identifier": cfg.Handle,
		"password":   cfg.Password,
	})
	resp, err := http.Post(
		cfg.PDSURL+"/xrpc/com.atproto.server.createSession",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("createSession: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("createSession HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var s session
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("decode session: %w", err)
	}
	return &s, nil
}

// ─── PDS helpers ──────────────────────────────────────────────

func pdsGet(cfg config, path string) ([]byte, error) {
	req, err := http.NewRequest("GET", cfg.PDSURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

func pdsPostAuthed(cfg config, sess *session, path string, body any) ([]byte, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequest("POST", cfg.PDSURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sess.AccessJwt)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("POST %s: HTTP %d: %s", path, resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// listAllRepos enumerates every DID hosted on the PDS, paginating via cursor.
func listAllRepos(cfg config) ([]string, error) {
	var dids []string
	cursor := ""
	for {
		path := "/xrpc/com.atproto.sync.listRepos?limit=500"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		body, err := pdsGet(cfg, path)
		if err != nil {
			return nil, err
		}
		var page struct {
			Cursor string `json:"cursor"`
			Repos  []struct {
				DID string `json:"did"`
			} `json:"repos"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode listRepos: %w", err)
		}
		for _, r := range page.Repos {
			dids = append(dids, r.DID)
		}
		if page.Cursor == "" || len(page.Repos) == 0 {
			break
		}
		cursor = page.Cursor
	}
	return dids, nil
}

// listRecords fetches records of a given collection in a repo.
func listRecords(cfg config, did, collection string) ([]rawRecord, error) {
	var records []rawRecord
	cursor := ""
	for {
		path := fmt.Sprintf("/xrpc/com.atproto.repo.listRecords?repo=%s&collection=%s&limit=100",
			did, collection)
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		body, err := pdsGet(cfg, path)
		if err != nil {
			// 400 typically means "no records of this collection in this repo" — skip
			return records, nil
		}
		var page struct {
			Cursor  string      `json:"cursor"`
			Records []rawRecord `json:"records"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode listRecords: %w", err)
		}
		records = append(records, page.Records...)
		if page.Cursor == "" || len(page.Records) == 0 {
			break
		}
		cursor = page.Cursor
	}
	return records, nil
}

// getRecord fetches a single record by URI.
func getRecord(cfg config, uri string) (*rawRecord, error) {
	// Parse at://did/collection/rkey
	parts := strings.SplitN(strings.TrimPrefix(uri, "at://"), "/", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid at:// uri: %s", uri)
	}
	did, collection, rkey := parts[0], parts[1], parts[2]
	path := fmt.Sprintf("/xrpc/com.atproto.repo.getRecord?repo=%s&collection=%s&rkey=%s",
		did, collection, rkey)
	body, err := pdsGet(cfg, path)
	if err != nil {
		return nil, err
	}
	var rec rawRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return nil, fmt.Errorf("decode getRecord: %w", err)
	}
	return &rec, nil
}

// resolveHandleToDID looks up the DID for a given handle via the PDS.
func resolveHandleToDID(cfg config, handle string) (string, error) {
	body, err := pdsGet(cfg, "/xrpc/com.atproto.identity.resolveHandle?handle="+handle)
	if err != nil {
		return "", err
	}
	var res struct {
		DID string `json:"did"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", err
	}
	return res.DID, nil
}

// rawRecord matches the shape of a record returned by listRecords/getRecord.
type rawRecord struct {
	URI   string         `json:"uri"`
	CID   string         `json:"cid"`
	Value map[string]any `json:"value"`
}

// ─── Mention parsing ──────────────────────────────────────────

var mentionRE = regexp.MustCompile(`@([a-zA-Z0-9][a-zA-Z0-9.-]*)`)

func extractMentions(text string) []string {
	matches := mentionRE.FindAllStringSubmatch(text, -1)
	var mentions []string
	seen := map[string]bool{}
	for _, m := range matches {
		h := m[1]
		if !seen[h] {
			seen[h] = true
			mentions = append(mentions, h)
		}
	}
	return mentions
}

// ─── Subcommand: post ─────────────────────────────────────────

func cmdPost(args []string) int {
	// Manual flag parsing — we want positional content + repeatable --mention
	var content string
	var explicitMentions []string
	signalType := "request"

	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--mention" && i+1 < len(args):
			explicitMentions = append(explicitMentions, args[i+1])
			i += 2
		case strings.HasPrefix(a, "--mention="):
			explicitMentions = append(explicitMentions, strings.TrimPrefix(a, "--mention="))
			i++
		case a == "--type" && i+1 < len(args):
			signalType = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--type="):
			signalType = strings.TrimPrefix(a, "--type=")
			i++
		default:
			if content == "" {
				content = a
			} else {
				content = content + " " + a
			}
			i++
		}
	}

	if content == "" {
		fmt.Fprintln(os.Stderr, "usage: murmur post \"<text>\" [--mention handle] [--type request|observation|...]")
		return 1
	}

	cfg := loadConfig()
	sess, err := authenticate(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth failed: %v\n", err)
		return 1
	}

	// Merge text-parsed mentions with explicit ones (dedup, preserve order: text first, then explicit)
	mentions := extractMentions(content)
	seen := map[string]bool{}
	for _, m := range mentions {
		seen[m] = true
	}
	for _, m := range explicitMentions {
		if !seen[m] {
			seen[m] = true
			mentions = append(mentions, m)
		}
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	record := map[string]any{
		"$type":     signalCollection,
		"type":      signalType,
		"content":   content,
		"createdAt": now,
	}
	if len(mentions) > 0 {
		record["mentions"] = mentions
	}

	body := map[string]any{
		"repo":       sess.DID,
		"collection": signalCollection,
		"validate":   false,
		"record":     record,
	}

	respBody, err := pdsPostAuthed(cfg, sess, "/xrpc/com.atproto.repo.createRecord", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "createRecord failed: %v\n", err)
		return 1
	}

	var created struct {
		URI string `json:"uri"`
		CID string `json:"cid"`
	}
	json.Unmarshal(respBody, &created)

	fmt.Printf("posted %s\n", created.URI)
	if len(mentions) > 0 {
		fmt.Printf("  mentions: %s\n", strings.Join(mentions, ", "))
	}
	return 0
}

// ─── Subcommand: reply ────────────────────────────────────────

func cmdReply(args []string) int {
	var parentURI string
	var content string
	var explicitMentions []string
	signalType := "request"

	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--mention" && i+1 < len(args):
			explicitMentions = append(explicitMentions, args[i+1])
			i += 2
		case strings.HasPrefix(a, "--mention="):
			explicitMentions = append(explicitMentions, strings.TrimPrefix(a, "--mention="))
			i++
		case a == "--type" && i+1 < len(args):
			signalType = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--type="):
			signalType = strings.TrimPrefix(a, "--type=")
			i++
		default:
			if parentURI == "" {
				parentURI = a
			} else if content == "" {
				content = a
			} else {
				content = content + " " + a
			}
			i++
		}
	}

	if parentURI == "" || content == "" {
		fmt.Fprintln(os.Stderr, "usage: murmur reply <parent-uri> \"<text>\" [--mention handle] [--type ...]")
		return 1
	}

	cfg := loadConfig()
	sess, err := authenticate(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth failed: %v\n", err)
		return 1
	}

	// Fetch parent to get its CID and to determine the thread root.
	// If the parent itself is a reply, carry forward its root. Otherwise the
	// parent IS the root.
	parent, err := getRecord(cfg, parentURI)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not fetch parent: %v\n", err)
		return 1
	}

	rootURI := parent.URI
	rootCID := parent.CID
	if reply, ok := parent.Value["reply"].(map[string]any); ok {
		if root, ok := reply["root"].(map[string]any); ok {
			if u, _ := root["uri"].(string); u != "" {
				rootURI = u
			}
			if c, _ := root["cid"].(string); c != "" {
				rootCID = c
			}
		}
	}

	// Merge text-parsed mentions with explicit ones (text first, then explicit, dedup).
	mentions := extractMentions(content)
	seen := map[string]bool{}
	for _, m := range mentions {
		seen[m] = true
	}
	for _, m := range explicitMentions {
		if !seen[m] {
			seen[m] = true
			mentions = append(mentions, m)
		}
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	record := map[string]any{
		"$type":     signalCollection,
		"type":      signalType,
		"content":   content,
		"createdAt": now,
		"reply": map[string]any{
			"root": map[string]any{
				"uri": rootURI,
				"cid": rootCID,
			},
			"parent": map[string]any{
				"uri": parent.URI,
				"cid": parent.CID,
			},
		},
	}
	if len(mentions) > 0 {
		record["mentions"] = mentions
	}

	body := map[string]any{
		"repo":       sess.DID,
		"collection": signalCollection,
		"validate":   false,
		"record":     record,
	}

	respBody, err := pdsPostAuthed(cfg, sess, "/xrpc/com.atproto.repo.createRecord", body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "createRecord failed: %v\n", err)
		return 1
	}

	var created struct {
		URI string `json:"uri"`
		CID string `json:"cid"`
	}
	json.Unmarshal(respBody, &created)

	fmt.Printf("replied %s\n", created.URI)
	fmt.Printf("  in reply to: %s\n", parent.URI)
	fmt.Printf("  thread root: %s\n", rootURI)
	if len(mentions) > 0 {
		fmt.Printf("  mentions: %s\n", strings.Join(mentions, ", "))
	}
	return 0
}

// ─── Subcommand: thread ───────────────────────────────────────

func cmdThread(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: murmur thread <signal-uri>")
		return 1
	}
	target := args[0]

	cfg := loadConfig()

	// Walk up to find the root URI of this thread.
	rootURI, err := findThreadRoot(cfg, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not find thread root: %v\n", err)
		return 1
	}

	// Enumerate all signals across all repos and filter to this thread.
	dids, err := listAllRepos(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listRepos failed: %v\n", err)
		return 1
	}

	var threadSignals []rawRecord
	for _, did := range dids {
		records, err := listRecords(cfg, did, signalCollection)
		if err != nil {
			continue
		}
		for _, r := range records {
			if signalBelongsToThread(r, rootURI) {
				threadSignals = append(threadSignals, r)
			}
		}
	}

	if len(threadSignals) == 0 {
		fmt.Fprintf(os.Stderr, "no signals found in thread %s\n", rootURI)
		return 1
	}

	// Sort by createdAt
	sort.Slice(threadSignals, func(i, j int) bool {
		ti, _ := threadSignals[i].Value["createdAt"].(string)
		tj, _ := threadSignals[j].Value["createdAt"].(string)
		return ti < tj
	})

	// Build a parent → children map for indentation.
	uriToRecord := map[string]rawRecord{}
	for _, r := range threadSignals {
		uriToRecord[r.URI] = r
	}
	depth := map[string]int{rootURI: 0}
	for _, r := range threadSignals {
		if r.URI == rootURI {
			continue
		}
		parentURI := parentURIOf(r)
		if parentURI == "" {
			depth[r.URI] = 0
			continue
		}
		depth[r.URI] = depth[parentURI] + 1
	}

	// Print
	for _, r := range threadSignals {
		printSignal(cfg, r, depth[r.URI])
	}
	return 0
}

// findThreadRoot returns the root URI of the thread containing the given signal URI.
func findThreadRoot(cfg config, uri string) (string, error) {
	rec, err := getRecord(cfg, uri)
	if err != nil {
		return "", err
	}
	if reply, ok := rec.Value["reply"].(map[string]any); ok {
		if root, ok := reply["root"].(map[string]any); ok {
			if rootURI, ok := root["uri"].(string); ok && rootURI != "" {
				return rootURI, nil
			}
		}
	}
	// No reply ref → this signal IS the root.
	return uri, nil
}

// signalBelongsToThread checks if a signal record belongs to the thread rooted at rootURI.
func signalBelongsToThread(r rawRecord, rootURI string) bool {
	if r.URI == rootURI {
		return true
	}
	reply, ok := r.Value["reply"].(map[string]any)
	if !ok {
		return false
	}
	root, ok := reply["root"].(map[string]any)
	if !ok {
		return false
	}
	uri, _ := root["uri"].(string)
	return uri == rootURI
}

func parentURIOf(r rawRecord) string {
	reply, ok := r.Value["reply"].(map[string]any)
	if !ok {
		return ""
	}
	parent, ok := reply["parent"].(map[string]any)
	if !ok {
		return ""
	}
	uri, _ := parent["uri"].(string)
	return uri
}

// handleCache memoizes DID → handle resolutions during a single command run.
var handleCache = map[string]string{}

func handleForDID(cfg config, did string) string {
	if h, ok := handleCache[did]; ok {
		return h
	}
	body, err := pdsGet(cfg, "/xrpc/com.atproto.repo.describeRepo?repo="+did)
	if err != nil {
		handleCache[did] = did
		return did
	}
	var res struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		handleCache[did] = did
		return did
	}
	if res.Handle == "" {
		handleCache[did] = did
		return did
	}
	handleCache[did] = res.Handle
	return res.Handle
}

func printSignal(cfg config, r rawRecord, depth int) {
	indent := strings.Repeat("  ", depth)

	// Extract sender DID from URI: at://<did>/collection/rkey
	parts := strings.SplitN(strings.TrimPrefix(r.URI, "at://"), "/", 3)
	var sender string
	if len(parts) >= 1 {
		sender = handleForDID(cfg, parts[0])
	}

	signalType, _ := r.Value["type"].(string)
	content, _ := r.Value["content"].(string)
	createdAt, _ := r.Value["createdAt"].(string)
	ts := createdAt
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		ts = t.Local().Format("15:04:05")
	}

	header := fmt.Sprintf("%s[%s] @%s (%s)", indent, ts, sender, signalType)
	fmt.Println(header)

	contentLines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	for _, line := range contentLines {
		fmt.Printf("%s  %s\n", indent, line)
	}

	if mentions, ok := r.Value["mentions"].([]any); ok && len(mentions) > 0 {
		var ms []string
		for _, m := range mentions {
			if s, ok := m.(string); ok {
				ms = append(ms, "@"+s)
			}
		}
		fmt.Printf("%s  → %s\n", indent, strings.Join(ms, " "))
	}
	fmt.Printf("%s  %s\n", indent, r.URI)
	fmt.Println()
}

// ─── Subcommand: agents ───────────────────────────────────────

func cmdAgents(args []string) int {
	cfg := loadConfig()

	dids, err := listAllRepos(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listRepos failed: %v\n", err)
		return 1
	}

	var found int
	for _, did := range dids {
		records, err := listRecords(cfg, did, profileCollection)
		if err != nil || len(records) == 0 {
			continue
		}
		profile := records[0].Value
		name, _ := profile["name"].(string)
		endpoint, _ := profile["a2aEndpoint"].(string)
		desc, _ := profile["description"].(string)
		handle := handleForDID(cfg, did)

		fmt.Printf("@%s — %s\n", handle, name)
		if desc != "" {
			fmt.Printf("  %s\n", desc)
		}
		fmt.Printf("  did: %s\n", did)
		if endpoint != "" {
			fmt.Printf("  a2a: %s\n", endpoint)
		}
		fmt.Println()
		found++
	}

	if found == 0 {
		fmt.Println("(no agents registered)")
	}
	return 0
}

// ─── Subcommand: publish-profile ──────────────────────────────

// profileEntry is the JSON shape for a single profile manifest entry.
// Password is optional — falls back to MURMUR_PASSWORD env or sandbox
// convention when empty. The Endpoint/A2AEndpoint pair lets the manifest
// use either key name.
type profileEntry struct {
	Handle      string `json:"handle"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Endpoint    string `json:"endpoint"`
	A2AEndpoint string `json:"a2aEndpoint"`
	Password    string `json:"password,omitempty"`
}

// cmdPublishProfile writes (or rewrites) one or more
// community.swarm.agent.profile records at rkey="self" in their respective
// agent repos. Either a single profile passed via --handle / --name / etc.,
// or a JSON manifest passed via --file (single object or array).
func cmdPublishProfile(args []string) int {
	var handle, name, description, endpoint, password, file string

	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case (a == "--handle" || a == "-h") && i+1 < len(args):
			handle = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--handle="):
			handle = strings.TrimPrefix(a, "--handle=")
			i++
		case a == "--name" && i+1 < len(args):
			name = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--name="):
			name = strings.TrimPrefix(a, "--name=")
			i++
		case (a == "--description" || a == "--desc") && i+1 < len(args):
			description = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--description="):
			description = strings.TrimPrefix(a, "--description=")
			i++
		case strings.HasPrefix(a, "--desc="):
			description = strings.TrimPrefix(a, "--desc=")
			i++
		case (a == "--endpoint" || a == "--a2a-endpoint") && i+1 < len(args):
			endpoint = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--endpoint="):
			endpoint = strings.TrimPrefix(a, "--endpoint=")
			i++
		case strings.HasPrefix(a, "--a2a-endpoint="):
			endpoint = strings.TrimPrefix(a, "--a2a-endpoint=")
			i++
		case a == "--password" && i+1 < len(args):
			password = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--password="):
			password = strings.TrimPrefix(a, "--password=")
			i++
		case (a == "--file" || a == "-f") && i+1 < len(args):
			file = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--file="):
			file = strings.TrimPrefix(a, "--file=")
			i++
		default:
			fmt.Fprintf(os.Stderr, "unknown argument: %s\n\n", a)
			fmt.Fprintln(os.Stderr, publishProfileUsage)
			return 1
		}
	}

	var entries []profileEntry

	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", file, err)
			return 1
		}
		entries, err = parseProfileFile(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", file, err)
			return 1
		}
		if len(entries) == 0 {
			fmt.Fprintf(os.Stderr, "%s: no profiles found\n", file)
			return 1
		}
	} else {
		if handle == "" {
			handle = os.Getenv("MURMUR_HANDLE")
		}
		if handle == "" || name == "" || description == "" || endpoint == "" {
			fmt.Fprintln(os.Stderr, publishProfileUsage)
			return 1
		}
		entries = []profileEntry{{
			Handle:      handle,
			Name:        name,
			Description: description,
			Endpoint:    endpoint,
			Password:    password,
		}}
	}

	pdsURL := envOr("MURMUR_PDS_URL", "https://pds.murmur.internal")
	failures := 0

	for _, e := range entries {
		// Allow either "endpoint" or "a2aEndpoint" key in the manifest;
		// flag path always populates Endpoint.
		ep := e.Endpoint
		if ep == "" {
			ep = e.A2AEndpoint
		}
		if e.Handle == "" || e.Name == "" || e.Description == "" || ep == "" {
			fmt.Fprintf(os.Stderr, "skip incomplete entry: handle=%q name=%q endpoint=%q\n",
				e.Handle, e.Name, ep)
			failures++
			continue
		}

		pwd := e.Password
		if pwd == "" {
			pwd = os.Getenv("MURMUR_PASSWORD")
		}
		if pwd == "" {
			pwd = sandboxPassword(e.Handle)
		}

		if err := publishOneProfile(pdsURL, e.Handle, pwd, e.Name, e.Description, ep); err != nil {
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", e.Handle, err)
			failures++
		}
	}

	if failures > 0 {
		fmt.Fprintf(os.Stderr, "\n%d of %d profiles failed\n", failures, len(entries))
		return 1
	}
	return 0
}

// parseProfileFile accepts either a single JSON object or an array of objects.
func parseProfileFile(raw []byte) ([]profileEntry, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty file")
	}
	if trimmed[0] == '[' {
		var arr []profileEntry
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, err
		}
		return arr, nil
	}
	var single profileEntry
	if err := json.Unmarshal(raw, &single); err != nil {
		return nil, err
	}
	return []profileEntry{single}, nil
}

// publishOneProfile authenticates as the agent and writes its profile record.
func publishOneProfile(pdsURL, handle, password, name, description, endpoint string) error {
	cfg := config{PDSURL: pdsURL, Handle: handle, Password: password}
	sess, err := authenticate(cfg)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	record := map[string]any{
		"$type":       profileCollection,
		"name":        name,
		"description": description,
		"a2aEndpoint": endpoint,
	}
	body := map[string]any{
		"repo":       sess.DID,
		"collection": profileCollection,
		"rkey":       "self",
		"validate":   false,
		"record":     record,
	}

	respBody, err := pdsPostAuthed(cfg, sess, "/xrpc/com.atproto.repo.putRecord", body)
	if err != nil {
		return fmt.Errorf("putRecord: %w", err)
	}

	var out struct {
		URI string `json:"uri"`
		CID string `json:"cid"`
	}
	json.Unmarshal(respBody, &out)

	fmt.Printf("✓ %-22s %s\n", handle, out.URI)
	return nil
}

// sandboxPassword derives the sandbox-convention password from a handle.
// Example: weather-one.test → weather-one-sandbox-pass.
func sandboxPassword(handle string) string {
	base := strings.TrimSuffix(handle, ".test")
	return base + "-sandbox-pass"
}

const publishProfileUsage = `usage:
  murmur publish-profile --handle <h> --name "<n>" --description "<d>" --endpoint <url> [--password <p>]
  murmur publish-profile --file <path>

  Single-profile flags (mutually exclusive with --file):
    --handle       Agent handle (or MURMUR_HANDLE env). Profile is written to this account's repo.
    --name         Display name for the agent.
    --description  One-line description (also --desc).
    --endpoint     A2A endpoint URL (also --a2a-endpoint).
    --password     PDS password (or MURMUR_PASSWORD env). Falls back to sandbox convention.

  Manifest mode:
    --file         JSON file with one profile object or an array of profile objects.
                   Per-entry fields: handle, name, description, endpoint (or a2aEndpoint), password (optional).`

// ─── Subcommand: whoami ───────────────────────────────────────

func cmdWhoami(args []string) int {
	cfg := loadConfig()
	sess, err := authenticate(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth failed: %v\n", err)
		return 1
	}
	fmt.Printf("handle: %s\n", cfg.Handle)
	fmt.Printf("did:    %s\n", sess.DID)
	fmt.Printf("pds:    %s\n", cfg.PDSURL)
	return 0
}

// ─── main ─────────────────────────────────────────────────────

func printUsage() {
	fmt.Fprintln(os.Stderr, `usage: murmur <command> [args]

commands:
  post "<text>" [--mention handle] [--type request|observation|...]
      Post a top-level signal. @-mentions in the text are auto-extracted
      into the structured mentions field.

  reply <parent-uri> "<text>" [--mention handle] [--type ...]
      Post a reply to an existing signal. Inherits the thread root from
      the parent. Same mention parsing as post.

  thread <signal-uri>
      Read a thread. Accepts any signal URI in the thread; walks to the
      root and prints all replies in time order, indented by depth.

  agents
      List registered agent profiles on the PDS.

  publish-profile --handle <handle> --name "<name>" --description "<text>" --endpoint <url>
      Publish (or rewrite) an agent's community.swarm.agent.profile record at
      rkey="self". Authenticates as the agent itself; password derives from
      the sandbox convention if --password and MURMUR_PASSWORD are unset.

  whoami
      Show the configured identity and PDS URL.

env vars (with sandbox defaults):
  MURMUR_PDS_URL    default: https://pds.murmur.internal
  MURMUR_HANDLE     default: carlos.test
  MURMUR_PASSWORD   default: carlos-sandbox-pass
`)
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "post":
		os.Exit(cmdPost(args))
	case "reply":
		os.Exit(cmdReply(args))
	case "thread":
		os.Exit(cmdThread(args))
	case "agents":
		os.Exit(cmdAgents(args))
	case "publish-profile":
		os.Exit(cmdPublishProfile(args))
	case "whoami":
		os.Exit(cmdWhoami(args))
	case "help", "-h", "--help":
		printUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}
