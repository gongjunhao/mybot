package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LoadDotEnv loads KEY=VALUE pairs from a .env file into the process environment.
// It does not override already-set environment variables.
// This is intentionally minimal to avoid extra deps.
func LoadDotEnv(path string) error {
	if strings.TrimSpace(path) == "" {
		path = ".env"
	}
	override := envBool("DOTENV_OVERRIDE", false)
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		// Missing .env is fine.
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		// Respect existing env unless override is enabled.
		if !override {
			if _, exists := os.LookupEnv(k); exists {
				continue
			}
		} else {
			// Allow overriding existing value.
			_ = os.Unsetenv(k)
		}
		if _, exists := os.LookupEnv(k); exists && !override {
			continue
		}
		v = strings.TrimSpace(v)
		v = stripInlineComment(v)
		v = trimQuotes(v)
		_ = os.Setenv(k, v)
	}
	return sc.Err()
}

func stripInlineComment(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return v
	}
	// If value is quoted, keep as-is (including '#').
	if v[0] == '"' || v[0] == '\'' {
		return v
	}
	// Common .env pattern: KEY=value # comment
	if i := strings.IndexByte(v, '#'); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

func trimQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
