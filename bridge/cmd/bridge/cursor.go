package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// loadCursor reads the persisted firehose cursor from disk. Returns (seq, true)
// if the cursor file exists and parses; (0, false) otherwise (file missing,
// empty, or corrupt — caller should subscribe from "live tail").
func loadCursor(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return 0, false
	}
	seq, err := strconv.ParseInt(s, 10, 64)
	if err != nil || seq <= 0 {
		return 0, false
	}
	return seq, true
}

// saveCursor writes seq to path, replacing any existing content. Errors are
// returned to the caller; on partial-write crashes the next loadCursor will
// fall back to live tail and the firehose will resume from there.
func saveCursor(path string, seq int64) error {
	if seq <= 0 {
		return nil
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", seq)), 0644)
}
