package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"mybot/internal/config"
	"mybot/internal/core"
)

type ScheduleStore struct {
	path string
	mu   sync.Mutex
	data schedulesFile
}

type schedulesFile struct {
	Tasks []ScheduledTask `json:"tasks"`
}

type ScheduledTask struct {
	ID         string    `json:"id"`
	ChatID     int64     `json:"chat_id"`
	DailyHHMM  string    `json:"daily_hhmm"` // "09:00"
	Prompt     string    `json:"prompt"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	LastRunYMD string    `json:"last_run_ymd"` // "YYYY-MM-DD" (local)
}

func NewScheduleStore(cfg config.Config) *ScheduleStore {
	p := filepath.Join(cfg.LogDir, "schedules.json")
	s := &ScheduleStore{path: p}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = s.load()
	return s
}

func (s *ScheduleStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.data = schedulesFile{}
			return nil
		}
		return err
	}
	var f schedulesFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	s.data = f
	return nil
}

func (s *ScheduleStore) saveLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *ScheduleStore) List(chatID int64) []ScheduledTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ScheduledTask
	for _, t := range s.data.Tasks {
		if t.ChatID == chatID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *ScheduleStore) UpsertDaily(chatID int64, hhmm string, prompt string) (ScheduledTask, error) {
	h, m, err := parseHHMM(hhmm)
	if err != nil {
		return ScheduledTask{}, err
	}
	hhmm = fmt.Sprintf("%02d:%02d", h, m)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ScheduledTask{}, errors.New("empty prompt")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		t := &s.data.Tasks[i]
		if t.ChatID == chatID && t.DailyHHMM == hhmm {
			t.Prompt = prompt
			t.Enabled = true
			_ = s.saveLocked()
			return *t, nil
		}
	}
	t := ScheduledTask{
		ID:         fmt.Sprintf("%d", time.Now().UnixNano()),
		ChatID:     chatID,
		DailyHHMM:  hhmm,
		Prompt:     prompt,
		Enabled:    true,
		CreatedAt:  time.Now(),
		LastRunYMD: "",
	}
	s.data.Tasks = append(s.data.Tasks, t)
	_ = s.saveLocked()
	return t, nil
}

func (s *ScheduleStore) Remove(chatID int64, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, errors.New("empty id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j := 0
	removed := false
	for _, t := range s.data.Tasks {
		if t.ChatID == chatID && t.ID == id {
			removed = true
			continue
		}
		s.data.Tasks[j] = t
		j++
	}
	s.data.Tasks = s.data.Tasks[:j]
	if removed {
		_ = s.saveLocked()
	}
	return removed, nil
}

func (s *ScheduleStore) SetEnabled(chatID int64, id string, enabled bool) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, errors.New("empty id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ChatID == chatID && s.data.Tasks[i].ID == id {
			s.data.Tasks[i].Enabled = enabled
			_ = s.saveLocked()
			return true, nil
		}
	}
	return false, nil
}

func (s *ScheduleStore) markRan(id string, ymd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ID == id {
			s.data.Tasks[i].LastRunYMD = ymd
			_ = s.saveLocked()
			return
		}
	}
}

func RunScheduler(ctx context.Context, bot *tgbotapi.BotAPI, cfg config.Config, sessions *core.SessionManager, store *ScheduleStore) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			hhmm := fmt.Sprintf("%02d:%02d", now.Hour(), now.Minute())
			today := now.Format("2006-01-02")

			// copy snapshot to avoid holding lock while running tasks
			store.mu.Lock()
			tasks := append([]ScheduledTask(nil), store.data.Tasks...)
			store.mu.Unlock()

			for _, t := range tasks {
				if !t.Enabled {
					continue
				}
				// Safety: only allowlist chat_ids.
				if _, ok := cfg.Allowlist[t.ChatID]; !ok {
					continue
				}
				if t.DailyHHMM != hhmm {
					continue
				}
				if t.LastRunYMD == today {
					continue
				}

				// mark before running to avoid duplicates if execution is long
				store.markRan(t.ID, today)

				go func(task ScheduledTask) {
					s, err := sessions.Send(ctx, task.ChatID, task.Prompt)
					if err != nil {
						sendText(bot, task.ChatID, fmt.Sprintf("schedule run failed: %v", err))
						return
					}
					go pumpEvents(bot, cfg, task.ChatID, s)
				}(t)
			}
		}
	}
}

func parseHHMM(hhmm string) (int, int, error) {
	hhmm = strings.TrimSpace(hhmm)
	parts := strings.Split(hhmm, ":")
	if len(parts) != 2 {
		return 0, 0, errors.New("time must be HH:MM")
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, errors.New("bad hour")
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, errors.New("bad minute")
	}
	return h, m, nil
}
