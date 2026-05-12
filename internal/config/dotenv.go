package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ParseDotEnv reads a .env file consisting of KEY=VALUE lines and returns the
// key/value pairs as a map. Blank lines and lines whose first non-space
// character is '#' are ignored. A leading "export " on a line is stripped.
// Surrounding single or double quotes are removed from values; no shell
// expansion, interpolation, or escape processing is performed.
//
// A missing file returns an error that tells the user to create one.
func ParseDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no .env file at %s — create one (see .env.example)", path)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	defer f.Close()

	out := make(map[string]string)
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("%s:%d: not a KEY=VALUE line: %q", path, lineNo, line)
		}
		key := strings.TrimSpace(line[:eq])
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty key", path, lineNo)
		}
		val := strings.TrimSpace(line[eq+1:])
		val = unquote(val)
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return out, nil
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
