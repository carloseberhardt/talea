package main

import (
	"bufio"
	"os"
	"strings"
)

// loadDotEnv reads a .env-style file and sets each KEY=VALUE pair as a process
// environment variable, but only if that variable isn't already set in the
// environment. This makes the file act as a default — anything set in the
// shell still wins.
//
// Format:
//   - Lines like KEY=VALUE
//   - Blank lines and lines starting with `#` are skipped
//   - Surrounding double quotes around the value are stripped
//   - Whitespace around the key and = is trimmed
//
// A missing file is not an error. Returns the path that was actually loaded,
// or "" if no file was loaded.
func loadDotEnv(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"`)
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		os.Setenv(key, val)
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return path, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
