package telegram

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"mybot/internal/config"
	"mybot/internal/core"
	"mybot/internal/util"
)

func Run(ctx context.Context, cfg config.Config, sessions *core.SessionManager) error {
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return err
	}
	bot.Debug = false

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := bot.GetUpdatesChan(u)

	log.Printf("telegram: started as @%s", bot.Self.UserName)

	for {
		select {
		case <-ctx.Done():
			return nil
		case up := <-updates:
			if up.Message == nil {
				continue
			}
			chatID := up.Message.Chat.ID
			if _, ok := cfg.Allowlist[chatID]; !ok {
				if cfg.LogUnknown {
					log.Printf("telegram: ignored chat_id=%d user=%s text=%q", chatID, userLabel(up.Message), up.Message.Text)
				}
				// Ignore silently for safety.
				continue
			}
			handleMessage(ctx, bot, cfg, sessions, up.Message)
		}
	}
}

func userLabel(m *tgbotapi.Message) string {
	if m.From == nil {
		return ""
	}
	u := m.From
	if u.UserName != "" {
		return "@" + u.UserName
	}
	if u.FirstName != "" || u.LastName != "" {
		return strings.TrimSpace(u.FirstName + " " + u.LastName)
	}
	return fmt.Sprintf("%d", u.ID)
}

func handleMessage(ctx context.Context, bot *tgbotapi.BotAPI, cfg config.Config, sessions *core.SessionManager, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	// Document upload support.
	if msg.Document != nil {
		prompt, err := saveAndBuildPrompt(ctx, bot, cfg, msg)
		if err != nil {
			sendText(bot, chatID, fmt.Sprintf("file save failed: %v", err))
			return
		}
		// If user also typed a caption, append it.
		if strings.TrimSpace(msg.Caption) != "" {
			prompt += "\n\nUser caption:\n" + msg.Caption
		}
		s, err := sessions.Send(ctx, chatID, prompt)
		if err != nil {
			sendText(bot, chatID, fmt.Sprintf("send failed: %v", err))
			if s == nil {
				return
			}
		}
		go pumpEvents(bot, cfg, chatID, s)
		return
	}

	text := msg.Text
	if text == "" {
		return
	}

	if strings.HasPrefix(text, "/") {
		cmd := strings.Fields(text)
		switch cmd[0] {
		case "/new":
			s, err := sessions.NewFresh(ctx, chatID)
			if err != nil {
				sendText(bot, chatID, fmt.Sprintf("new session failed: %v", err))
				return
			}
			sendText(bot, chatID, fmt.Sprintf("session: %s", s.SessionID))
			go pumpEvents(bot, cfg, chatID, s)
			return
		case "/cancel":
			_ = sessions.Cancel(chatID)
			sendText(bot, chatID, "sent interrupt")
			return
		case "/status":
			st, ok := sessions.Status(chatID)
			if !ok {
				sendText(bot, chatID, "no session")
				return
			}
			sendText(bot, chatID, st)
			return
		case "/help":
			sendText(bot, chatID, "/new /cancel /status /uploads /delete <name-or-path>\n/skills [/ls]\n/skills install <git-url-or-local-path> [name]\n/skills rm <name>\n/skills path")
			return
		case "/skills":
			handleSkillsCmd(bot, cfg, chatID, cmd)
			return
		case "/uploads":
			root := uploadsRoot(cfg)
			names, err := listUploads(root, cfg.UploadDir, 20)
			if err != nil {
				sendText(bot, chatID, fmt.Sprintf("uploads: %v", err))
				return
			}
			if len(names) == 0 {
				sendText(bot, chatID, "uploads: (empty)")
				return
			}
			sendText(bot, chatID, "uploads:\n- "+strings.Join(names, "\n- "))
			return
		case "/delete", "/rm":
			if len(cmd) < 2 {
				sendText(bot, chatID, "usage: /delete <filename|relative-path|absolute-path>")
				return
			}
			target, err := deleteUpload(cfg, strings.Join(cmd[1:], " "))
			if err != nil {
				sendText(bot, chatID, fmt.Sprintf("delete failed: %v", err))
				return
			}
			sendText(bot, chatID, fmt.Sprintf("deleted: %s", target))
			return
		default:
			sendText(bot, chatID, "unknown command; try /help")
			return
		}
	}

	// Natural-language delete helper (opt-in by wording).
	// We keep this conservative and only delete inside UPLOAD_DIR.
	if arg, ok := nlDeleteArg(text); ok {
		target, err := deleteUpload(cfg, arg)
		if err != nil {
			sendText(bot, chatID, fmt.Sprintf("delete failed: %v", err))
			return
		}
		sendText(bot, chatID, fmt.Sprintf("deleted: %s", target))
		return
	}

	s, err := sessions.Send(ctx, chatID, text)
	if err != nil {
		sendText(bot, chatID, fmt.Sprintf("send failed: %v", err))
		if s == nil {
			return
		}
	}
	go pumpEvents(bot, cfg, chatID, s)
}

// pumpEvents reads session events and posts to Telegram with batching.
// For simplicity (single-user) we allow repeated pumpers; util.DedupeGate avoids spamming.
func pumpEvents(bot *tgbotapi.BotAPI, cfg config.Config, chatID int64, s *core.Session) {
	if s == nil {
		return
	}
	gate := util.GetGate(s.SessionID)
	if !gate.TryEnter() {
		return
	}
	defer gate.Leave()

	ticker := time.NewTicker(cfg.FlushInterval)
	defer ticker.Stop()

	var buf strings.Builder
	flush := func(force bool) {
		if buf.Len() == 0 {
			return
		}
		if !force && buf.Len() < cfg.MaxChunkBytes {
			return
		}
		out := buf.String()
		buf.Reset()
		sendText(bot, chatID, util.TrimToBytes(out, cfg.MaxChunkBytes))
	}

	events := s.Events()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				s.MarkStopped("events closed")
				flush(true)
				return
			}
			switch ev.Type {
			case core.EventStdout, core.EventStderr, core.EventStatus:
				if cfg.HideStatus && ev.Type == core.EventStatus {
					break
				}
				buf.WriteString(util.StripANSI(ev.Text))
				if buf.Len() >= cfg.MaxChunkBytes {
					flush(true)
				}
			case core.EventExit:
				s.MarkStopped("")
				buf.WriteString(fmt.Sprintf("\n[exit code %d]\n", ev.Code))
				flush(true)
				return
			}
		case <-ticker.C:
			flush(true)
		}
	}
}

func sendText(bot *tgbotapi.BotAPI, chatID int64, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	body, _ := util.FormatTelegramHTML(text)
	m := tgbotapi.NewMessage(chatID, body)
	m.ParseMode = "HTML"
	_, _ = bot.Send(m)
}

func handleSkillsCmd(bot *tgbotapi.BotAPI, cfg config.Config, chatID int64, cmd []string) {
	if len(cmd) == 1 || (len(cmd) >= 2 && (cmd[1] == "ls" || cmd[1] == "list")) {
		names, err := listSkills(cfg)
		if err != nil {
			sendText(bot, chatID, fmt.Sprintf("skills: %v", err))
			return
		}
		if len(names) == 0 {
			sendText(bot, chatID, "skills: (empty)")
			return
		}
		sendText(bot, chatID, "skills:\n- "+strings.Join(names, "\n- "))
		return
	}

	switch cmd[1] {
	case "path":
		sendText(bot, chatID, "skills dir: "+skillsRoot(cfg))
		return
	case "rm", "remove", "delete", "del":
		if len(cmd) < 3 {
			sendText(bot, chatID, "usage: /skills rm <name>")
			return
		}
		name, err := removeSkill(cfg, cmd[2])
		if err != nil {
			sendText(bot, chatID, fmt.Sprintf("skills rm failed: %v", err))
			return
		}
		sendText(bot, chatID, "skills removed: "+name)
		return
	case "install", "add":
		if len(cmd) < 3 {
			sendText(bot, chatID, "usage: /skills install <git-url-or-local-path> [name]")
			return
		}
		source := cmd[2]
		name := ""
		if len(cmd) >= 4 {
			name = cmd[3]
		}
		installed, err := installSkill(cfg, name, source)
		if err != nil {
			// installSkill may return success-with-warning via err; include message.
			sendText(bot, chatID, fmt.Sprintf("skills install: %v", err))
			if installed != "" {
				sendText(bot, chatID, "installed: "+installed)
			}
			return
		}
		sendText(bot, chatID, "installed: "+installed)
		return
	default:
		sendText(bot, chatID, "usage:\n/skills\n/skills install <git-url-or-local-path> [name]\n/skills rm <name>\n/skills path")
		return
	}
}

func saveAndBuildPrompt(ctx context.Context, bot *tgbotapi.BotAPI, cfg config.Config, msg *tgbotapi.Message) (string, error) {
	chatID := msg.Chat.ID
	doc := msg.Document
	if doc == nil {
		return "", fmt.Errorf("no document")
	}
	if doc.FileSize > int(cfg.MaxUploadBytes) && cfg.MaxUploadBytes > 0 {
		return "", fmt.Errorf("file too large: %d bytes (max %d)", doc.FileSize, cfg.MaxUploadBytes)
	}

	workdir := cfg.WorkDir
	uploadDir := filepath.Join(workdir, cfg.UploadDir)
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return "", err
	}

	dstName := util.UniqueUploadName(doc.FileName)
	dstPath := filepath.Join(uploadDir, dstName)

	f, err := bot.GetFile(tgbotapi.FileConfig{FileID: doc.FileID})
	if err != nil {
		return "", err
	}
	url := f.Link(bot.Token)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}

	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer out.Close()

	var r io.Reader = resp.Body
	if cfg.MaxUploadBytes > 0 {
		r = io.LimitReader(resp.Body, cfg.MaxUploadBytes+1)
	}
	n, err := io.Copy(out, r)
	if err != nil {
		return "", err
	}
	if cfg.MaxUploadBytes > 0 && n > cfg.MaxUploadBytes {
		return "", fmt.Errorf("file too large: %d bytes (max %d)", n, cfg.MaxUploadBytes)
	}

	// User-facing confirmation.
	rel := filepath.ToSlash(filepath.Join(cfg.UploadDir, dstName))
	sendText(bot, chatID, fmt.Sprintf("saved: %s", rel))

	ext := strings.ToLower(filepath.Ext(dstPath))
	if ext == ".patch" || ext == ".diff" {
		return fmt.Sprintf("User uploaded a patch file saved at: %s\nPlease read it and summarize what it changes. Do not apply it unless explicitly asked.", rel), nil
	}
	return fmt.Sprintf("User uploaded a file saved at: %s\nPlease read it and use it as context.", rel), nil
}
