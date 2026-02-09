package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"mybot/internal/adapters/codex"
	"mybot/internal/config"
	"mybot/internal/core"
	"mybot/internal/telegram"
)

func main() {
	_ = config.LoadDotEnv(".env")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	adapter := codex.New(cfg.CodexCmd, cfg.CodexArgs, cfg.WorkDir, cfg.LogDir)
	sessions := core.NewSessionManager(adapter, cfg)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := telegram.Run(ctx, cfg, sessions); err != nil {
		msg := err.Error()
		if cfg.TelegramToken != "" {
			msg = strings.ReplaceAll(msg, cfg.TelegramToken, "<redacted>")
		}
		log.Fatalf("telegram: %s", msg)
	}
}
