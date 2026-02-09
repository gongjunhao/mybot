package telegram

import (
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"mybot/internal/config"
	"mybot/internal/util"
)

func handleMemoryCmd(bot *tgbotapi.BotAPI, cfg config.Config, chatID int64, cmd []string) {
	ms := NewMemoryStore(cfg)
	mem, err := ms.Get(chatID)
	if err != nil {
		sendText(bot, chatID, fmt.Sprintf("memory: %v", err))
		return
	}
	if mem == nil {
		sendText(bot, chatID, "memory: (empty)")
		return
	}

	// /memory ideas
	if len(cmd) >= 2 && (cmd[1] == "ideas" || cmd[1] == "idea") {
		if len(mem.SkillIdeas) == 0 {
			sendText(bot, chatID, "memory ideas: (empty)")
			return
		}
		var b strings.Builder
		b.WriteString("memory ideas:\n")
		for i, it := range mem.SkillIdeas {
			it = strings.TrimSpace(it)
			if it == "" {
				continue
			}
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, it))
		}
		sendText(bot, chatID, b.String())
		return
	}

	// Default: show summary + rules + prefs.
	var b strings.Builder
	if strings.TrimSpace(mem.Summary) != "" {
		b.WriteString("summary:\n")
		b.WriteString(mem.Summary)
		b.WriteString("\n\n")
	}
	if len(mem.Rules) > 0 {
		b.WriteString("rules:\n")
		for _, r := range mem.Rules {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			b.WriteString("- ")
			b.WriteString(r)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(mem.Prefs) > 0 {
		b.WriteString("prefs:\n")
		for _, p := range mem.Prefs {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			b.WriteString("- ")
			b.WriteString(p)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("tips:\n")
	b.WriteString("- /memory ideas 查看可沉淀为 skills 的方向\n")
	b.WriteString("- /skillify <name> <ideaIndex> 生成或升级 skill\n")

	sendText(bot, chatID, util.TrimToBytes(b.String(), cfg.MaxChunkBytes))
}
