package telegram

import (
	"context"
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"mybot/internal/config"
	"mybot/internal/core"
)

func handleScheduleCmd(bot *tgbotapi.BotAPI, cfg config.Config, sessions *core.SessionManager, store *ScheduleStore, chatID int64, cmd []string) {
	if store == nil {
		sendText(bot, chatID, "schedule store not initialized")
		return
	}
	if len(cmd) == 1 || (len(cmd) >= 2 && (cmd[1] == "ls" || cmd[1] == "list")) {
		tasks := store.List(chatID)
		if len(tasks) == 0 {
			sendText(bot, chatID, "schedule: (empty)")
			return
		}
		var b strings.Builder
		b.WriteString("schedule:\n")
		for _, t := range tasks {
			ena := "off"
			if t.Enabled {
				ena = "on"
			}
			b.WriteString(fmt.Sprintf("- id=%s %s %s last=%s\n", t.ID, t.DailyHHMM, ena, t.LastRunYMD))
		}
		sendText(bot, chatID, b.String())
		return
	}

	switch cmd[1] {
	case "add", "set":
		// Support both:
		// 1) /schedule add HH:MM <prompt>
		// 2) /schedule add 每天下午4点提醒我喝水
		if len(cmd) >= 4 {
			hhmm := cmd[2]
			prompt := strings.Join(cmd[3:], " ")
			task, err := store.UpsertDaily(chatID, hhmm, prompt)
			if err != nil {
				sendText(bot, chatID, fmt.Sprintf("schedule add failed: %v", err))
				return
			}
			sendText(bot, chatID, fmt.Sprintf("scheduled: id=%s daily %s", task.ID, task.DailyHHMM))
			return
		}

		if len(cmd) >= 3 {
			nl := strings.Join(cmd[2:], " ")
			nl = strings.TrimSpace(nl)
			if nl != "" && !strings.HasPrefix(nl, "每天") {
				nl = "每天" + nl
			}
			if ts, ok := parseDailySchedules(nl); ok {
				var ids []string
				for _, t := range ts {
					task, err := store.UpsertDaily(chatID, t.HHMM, t.Prompt)
					if err != nil {
						sendText(bot, chatID, fmt.Sprintf("schedule add failed: %v", err))
						return
					}
					ids = append(ids, fmt.Sprintf("%s(%s)", task.ID, task.DailyHHMM))
				}
				sendText(bot, chatID, "scheduled: "+strings.Join(ids, ", "))
				return
			}
		}

		sendText(bot, chatID, "usage: /schedule add HH:MM <prompt>\n或：/schedule add 每天下午4点提醒我喝水")
		return
	case "rm", "remove", "delete", "del":
		if len(cmd) < 3 {
			sendText(bot, chatID, "usage: /schedule rm <id>")
			return
		}
		ok, err := store.Remove(chatID, cmd[2])
		if err != nil {
			sendText(bot, chatID, fmt.Sprintf("schedule rm failed: %v", err))
			return
		}
		if !ok {
			sendText(bot, chatID, "schedule rm: not found")
			return
		}
		sendText(bot, chatID, "schedule removed")
		return
	case "on":
		if len(cmd) < 3 {
			sendText(bot, chatID, "usage: /schedule on <id>")
			return
		}
		ok, err := store.SetEnabled(chatID, cmd[2], true)
		if err != nil {
			sendText(bot, chatID, fmt.Sprintf("schedule on failed: %v", err))
			return
		}
		if !ok {
			sendText(bot, chatID, "schedule on: not found")
			return
		}
		sendText(bot, chatID, "schedule on: ok")
		return
	case "off":
		if len(cmd) < 3 {
			sendText(bot, chatID, "usage: /schedule off <id>")
			return
		}
		ok, err := store.SetEnabled(chatID, cmd[2], false)
		if err != nil {
			sendText(bot, chatID, fmt.Sprintf("schedule off failed: %v", err))
			return
		}
		if !ok {
			sendText(bot, chatID, "schedule off: not found")
			return
		}
		sendText(bot, chatID, "schedule off: ok")
		return
	case "run":
		// Manual trigger: /schedule run <id>
		if len(cmd) < 3 {
			sendText(bot, chatID, "usage: /schedule run <id>")
			return
		}
		tasks := store.List(chatID)
		for _, t := range tasks {
			if t.ID == cmd[2] {
				s, err := sessions.Send(context.Background(), chatID, t.Prompt)
				if err != nil {
					sendText(bot, chatID, fmt.Sprintf("schedule run failed: %v", err))
					return
				}
				go pumpEvents(bot, cfg, chatID, s)
				return
			}
		}
		sendText(bot, chatID, "schedule run: not found")
		return
	default:
		sendText(bot, chatID, "usage:\n/schedule\n/schedule add HH:MM <prompt>\n/schedule rm <id>\n/schedule on|off <id>\n/schedule run <id>")
		return
	}
}
