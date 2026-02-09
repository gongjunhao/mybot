package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mybot/internal/config"
)

type Adapter interface {
	Start(ctx context.Context, sessionID string) (Handle, error)
	Stop(h Handle) error
	Send(h Handle, input string) error
	Events(h Handle) <-chan Event
}

type Handle interface {
	SessionID() string
}

type EventType string

const (
	EventStdout EventType = "stdout"
	EventStderr EventType = "stderr"
	EventExit   EventType = "exit"
	EventStatus EventType = "status"
)

type Event struct {
	Type EventType
	Text string
	Time time.Time
	Code int
}

type SessionManager struct {
	adapter Adapter
	cfg     config.Config

	mu       sync.Mutex
	sessions map[int64]*Session // keyed by telegram chat_id (single-user: 1 active session per chat)
}

type Session struct {
	ChatID    int64
	SessionID string
	CreatedAt time.Time

	h        Handle
	events   <-chan Event
	lastErr  string
	lastSeen time.Time

	// serialize user input per session (avoid interleaving)
	inMu sync.Mutex

	runMu   sync.Mutex
	running bool
}

func NewSessionManager(adapter Adapter, cfg config.Config) *SessionManager {
	_ = os.MkdirAll(cfg.LogDir, 0o755)
	_ = os.MkdirAll(filepath.Join(cfg.LogDir, "sessions"), 0o755)
	return &SessionManager{
		adapter:  adapter,
		cfg:      cfg,
		sessions: make(map[int64]*Session),
	}
}

func (m *SessionManager) GetOrCreate(ctx context.Context, chatID int64) (*Session, error) {
	m.mu.Lock()
	s := m.sessions[chatID]
	m.mu.Unlock()
	if s != nil {
		return s, nil
	}
	return m.NewResume(ctx, chatID)
}

// NewFresh starts a brand new session and signals adapters to reset any persisted resume state.
func (m *SessionManager) NewFresh(ctx context.Context, chatID int64) (*Session, error) {
	sid := fmt.Sprintf("chat-%d-%d-fresh", chatID, time.Now().UnixNano())
	return m.newWithID(ctx, chatID, sid)
}

// NewResume starts a session that may resume from persisted state (if supported by the adapter).
func (m *SessionManager) NewResume(ctx context.Context, chatID int64) (*Session, error) {
	sid := fmt.Sprintf("chat-%d-%d", chatID, time.Now().UnixNano())
	return m.newWithID(ctx, chatID, sid)
}

func (m *SessionManager) newWithID(ctx context.Context, chatID int64, sid string) (*Session, error) {
	h, err := m.adapter.Start(ctx, sid)
	if err != nil {
		return nil, err
	}
	s := &Session{
		ChatID:    chatID,
		SessionID: sid,
		CreatedAt: time.Now(),
		h:         h,
		events:    m.adapter.Events(h),
		lastSeen:  time.Now(),
	}
	s.setRunning(true)

	var old Handle
	m.mu.Lock()
	if prev := m.sessions[chatID]; prev != nil {
		old = prev.h
	}
	m.sessions[chatID] = s
	m.mu.Unlock()

	if old != nil {
		_ = m.adapter.Stop(old)
	}
	return s, nil
}

func (m *SessionManager) Send(ctx context.Context, chatID int64, input string) (*Session, error) {
	s, err := m.GetOrCreate(ctx, chatID)
	if err != nil {
		return nil, err
	}
	s.inMu.Lock()
	defer s.inMu.Unlock()
	s.lastSeen = time.Now()
	if !s.IsRunning() {
		// restart session automatically (prefer resuming)
		s2, err := m.NewResume(ctx, chatID)
		if err != nil {
			return nil, err
		}
		s = s2
	}
	if err := m.adapter.Send(s.h, input); err != nil {
		s.lastErr = err.Error()
		return s, err
	}
	return s, nil
}

func (m *SessionManager) Cancel(chatID int64) error {
	m.mu.Lock()
	s := m.sessions[chatID]
	m.mu.Unlock()
	if s == nil {
		return nil
	}
	return m.adapter.Stop(s.h)
}

func (m *SessionManager) Status(chatID int64) (string, bool) {
	m.mu.Lock()
	s := m.sessions[chatID]
	m.mu.Unlock()
	if s == nil {
		return "no session", false
	}
	r := "stopped"
	if s.IsRunning() {
		r = "running"
	}
	if s.lastErr != "" {
		return fmt.Sprintf("%s (%s) lastErr=%s", s.SessionID, r, s.lastErr), true
	}
	return fmt.Sprintf("%s (%s)", s.SessionID, r), true
}

func (s *Session) Events() <-chan Event { return s.events }

func (s *Session) IsRunning() bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.running
}

func (s *Session) MarkStopped(reason string) {
	s.runMu.Lock()
	s.running = false
	s.runMu.Unlock()
	if reason != "" {
		s.lastErr = reason
	}
}

func (s *Session) setRunning(v bool) {
	s.runMu.Lock()
	s.running = v
	s.runMu.Unlock()
}
