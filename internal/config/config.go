package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TelegramToken string
	Allowlist     map[int64]struct{}
	LogUnknown    bool
	HideStatus    bool

	// CodexCmd/CodexArgs define the interactive CLI command to spawn.
	// Defaults to "codex". Args are appended after built-in fixed args in code.
	CodexCmd  string
	CodexArgs []string

	// Back-compat: older env var names.
	AdapterCmd  string
	AdapterArgs []string

	WorkDir        string
	UploadDir      string
	MaxUploadBytes int64
	SkillsDir      string

	// Output batching for Telegram.
	FlushInterval time.Duration
	MaxChunkBytes int

	// Safety.
	LogDir string
}

func Load() (Config, error) {
	var cfg Config

	cfg.TelegramToken = strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if cfg.TelegramToken == "" {
		return cfg, errors.New("missing TELEGRAM_BOT_TOKEN")
	}

	allow := strings.TrimSpace(os.Getenv("TELEGRAM_ALLOWLIST"))
	if allow == "" {
		return cfg, errors.New("missing TELEGRAM_ALLOWLIST (comma-separated chat_id list)")
	}
	al, err := parseAllowlist(allow)
	if err != nil {
		return cfg, fmt.Errorf("TELEGRAM_ALLOWLIST: %w", err)
	}
	cfg.Allowlist = al
	cfg.LogUnknown = envBool("TELEGRAM_LOG_UNKNOWN", false)
	cfg.HideStatus = envBool("TELEGRAM_HIDE_STATUS", false)

	cfg.CodexCmd = strings.TrimSpace(os.Getenv("CODEX_CMD"))
	cfg.CodexArgs = splitArgs(os.Getenv("CODEX_ARGS"))

	// Back-compat env vars.
	cfg.AdapterCmd = strings.TrimSpace(os.Getenv("ADAPTER_CMD"))
	cfg.AdapterArgs = splitArgs(os.Getenv("ADAPTER_ARGS"))

	if cfg.CodexCmd == "" {
		cfg.CodexCmd = strings.TrimSpace(os.Getenv("CODEX_BIN"))
	}
	if cfg.CodexCmd == "" {
		if cfg.AdapterCmd != "" {
			cfg.CodexCmd = cfg.AdapterCmd
		} else {
			cfg.CodexCmd = "codex"
		}
	}
	if len(cfg.CodexArgs) == 0 && len(cfg.AdapterArgs) != 0 {
		cfg.CodexArgs = cfg.AdapterArgs
	}

	cfg.WorkDir = strings.TrimSpace(os.Getenv("WORKDIR"))
	if cfg.WorkDir == "" {
		if wd, err := os.Getwd(); err == nil {
			cfg.WorkDir = wd
		}
	}
	cfg.UploadDir = strings.TrimSpace(os.Getenv("UPLOAD_DIR"))
	if cfg.UploadDir == "" {
		cfg.UploadDir = "uploads"
	}
	cfg.MaxUploadBytes = envInt64("MAX_UPLOAD_BYTES", 20*1024*1024) // 20MB

	cfg.SkillsDir = strings.TrimSpace(os.Getenv("SKILLS_DIR"))
	if cfg.SkillsDir == "" {
		if ch := strings.TrimSpace(os.Getenv("CODEX_HOME")); ch != "" {
			cfg.SkillsDir = filepath.Join(ch, "skills")
		} else if home, err := os.UserHomeDir(); err == nil && home != "" {
			cfg.SkillsDir = filepath.Join(home, ".codex", "skills")
		}
	}

	cfg.FlushInterval = envDuration("FLUSH_INTERVAL", 1200*time.Millisecond)
	cfg.MaxChunkBytes = envInt("MAX_CHUNK_BYTES", 3500) // keep under Telegram limits after escaping

	cfg.LogDir = strings.TrimSpace(os.Getenv("LOG_DIR"))
	if cfg.LogDir == "" {
		cfg.LogDir = "logs"
	}

	return cfg, nil
}

func parseAllowlist(s string) (map[int64]struct{}, error) {
	out := make(map[int64]struct{})
	parts := strings.Split(s, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad id %q", p)
		}
		out[id] = struct{}{}
	}
	if len(out) == 0 {
		return nil, errors.New("empty allowlist")
	}
	return out, nil
}

func envDuration(key string, def time.Duration) time.Duration {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

func envInt(key string, def int) int {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func envInt64(key string, def int64) int64 {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	switch strings.ToLower(s) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return def
	}
}

func splitArgs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// Simple split: space-separated; if you need quoting, wrap a tiny shell script as CODEX_CMD.
	return strings.Fields(s)
}
