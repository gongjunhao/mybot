package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mybot/internal/config"
)

type MemoryStore struct {
	path string
}

type memoryFile struct {
	Chats map[string]*chatMemory `json:"chats"`
}

type chatMemory struct {
	Summary string   `json:"summary"`
	Rules   []string `json:"rules"`
	Prefs   []string `json:"prefs"`

	UpdatedAt   time.Time `json:"updated_at"`
	CompactedAt time.Time `json:"compacted_at"`

	TokensSinceCompact int `json:"tokens_since_compact"`
	TurnsSinceCompact  int `json:"turns_since_compact"`

	SkillIdeas []string `json:"skill_ideas"`
}

func NewMemoryStore(cfg config.Config) *MemoryStore {
	return &MemoryStore{path: filepath.Join(cfg.LogDir, "memory.json")}
}

func (m *MemoryStore) Get(chatID int64) (*chatMemory, error) {
	b, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f memoryFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.Chats == nil {
		return nil, nil
	}
	return f.Chats[chatKeyFromChatID(chatID)], nil
}

func chatKeyFromChatID(chatID int64) string {
	return fmt.Sprintf("%d", chatID)
}
