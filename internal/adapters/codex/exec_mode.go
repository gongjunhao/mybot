package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mybot/internal/core"
)

type handleExec struct {
	sessionID string
	chatKey   string
	logDir    string

	cmdPath    string
	globalArgs []string

	skipGitRepoCheck bool

	mu       sync.Mutex
	threadID string
	running  *exec.Cmd

	events chan core.Event
	once   sync.Once

	adapter *Adapter
}

func (h *handleExec) SessionID() string { return h.sessionID }

func (a *Adapter) startExec(ctx context.Context, sessionID string) (core.Handle, error) {
	chatKey, fresh := parseChatKey(sessionID)
	if fresh && chatKey != "" {
		a.clearThread(chatKey)
	}

	h := &handleExec{
		sessionID:        sessionID,
		chatKey:          chatKey,
		logDir:           a.logDir,
		cmdPath:          a.cmd,
		globalArgs:       a.args,
		skipGitRepoCheck: a.skipGitRepoCheck,
		events:           make(chan core.Event, 256),
		adapter:          a,
	}

	if chatKey != "" {
		if tid := a.getThread(chatKey); tid != "" {
			h.threadID = tid
			h.events <- core.Event{Type: core.EventStatus, Text: "resumed thread_id=" + tid + "\n", Time: time.Now()}
		}
	}

	h.events <- core.Event{Type: core.EventStatus, Text: "started mode=exec\n", Time: time.Now()}
	return h, nil
}

func (a *Adapter) sendExec(h core.Handle, input string) error {
	hh := h.(*handleExec)

	prompt := strings.TrimSpace(input)
	if prompt == "" {
		return nil
	}

	hh.mu.Lock()
	threadID := hh.threadID
	hh.mu.Unlock()

	var argv []string
	argv = append(argv, hh.globalArgs...)
	argv = append(argv, "exec")
	if threadID != "" {
		argv = append(argv, "resume")
	}
	argv = append(argv, "--json")
	if hh.skipGitRepoCheck {
		argv = append(argv, "--skip-git-repo-check")
	}

	if threadID != "" {
		// Usage: codex exec resume [OPTIONS] [SESSION_ID] [PROMPT]
		argv = append(argv, threadID, prompt)
	} else {
		// Usage: codex exec [OPTIONS] [PROMPT]
		argv = append(argv, prompt)
	}

	cmd := exec.CommandContext(context.Background(), hh.cmdPath, argv...)
	// Put in its own process group so /cancel can interrupt the whole tree.
	setSysProcAttr(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	hh.mu.Lock()
	hh.running = cmd
	hh.mu.Unlock()

	// Tee user prompt to transcript (helps debugging).
	hh.appendTranscript("\n> " + prompt + "\n")

	done := make(chan struct{})
	go func() {
		defer close(done)
		hh.readJSONL(stdout)
	}()
	go func() {
		hh.readStderr(stderr)
	}()

	err = cmd.Wait()

	hh.mu.Lock()
	if hh.running == cmd {
		hh.running = nil
	}
	hh.mu.Unlock()

	<-done

	if err != nil {
		// Still emit exit as an event, so telegram side shows completion.
		return err
	}
	return nil
}

func (a *Adapter) stopExec(h core.Handle) error {
	hh := h.(*handleExec)
	hh.mu.Lock()
	cmd := hh.running
	hh.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := killProcessGroup(cmd.Process.Pid)
	if err == nil {
		return nil
	}
	return cmd.Process.Signal(os.Interrupt)
}

func (a *Adapter) eventsExec(h core.Handle) <-chan core.Event {
	return h.(*handleExec).events
}

func (hh *handleExec) readJSONL(r io.ReadCloser) {
	defer func() { _ = r.Close() }()

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()

		var ev codexJSON
		if err := json.Unmarshal(line, &ev); err != nil {
			hh.emit(core.EventStderr, "bad json: "+err.Error()+"\n")
			continue
		}

		switch ev.Type {
		case "thread.started":
			if ev.ThreadID != "" {
				var toPersist string
				var chatKey string
				hh.mu.Lock()
				if hh.threadID == "" {
					hh.threadID = ev.ThreadID
					toPersist = ev.ThreadID
					chatKey = hh.chatKey
				}
				hh.mu.Unlock()
				if toPersist != "" && chatKey != "" && hh.adapter != nil {
					hh.adapter.setThread(chatKey, toPersist)
					hh.appendTranscript("[resume thread_id " + toPersist + "]\n")
				}
			}
		case "item.completed":
			if ev.Item != nil && ev.Item.Type == "agent_message" && ev.Item.Text != "" {
				txt := ev.Item.Text
				if !strings.HasSuffix(txt, "\n") {
					txt += "\n"
				}
				hh.appendTranscript(txt)
				hh.emit(core.EventStdout, txt)
			}
		case "turn.completed":
			// ignore
		default:
			// ignore other event types for now
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		hh.emit(core.EventStderr, "jsonl read error: "+err.Error()+"\n")
	}
}

func (hh *handleExec) readStderr(r io.ReadCloser) {
	defer func() { _ = r.Close() }()

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text() + "\n"
		// Codex sometimes prints internal state warnings/errors that don't affect the response.
		// Keep Telegram output clean by filtering known noisy lines.
		if isNoisyCodexStderr(line) {
			hh.appendTranscript("[filtered stderr] " + line)
			continue
		}
		hh.appendTranscript(line)
		hh.emit(core.EventStderr, line)
	}
}

func isNoisyCodexStderr(line string) bool {
	s := strings.ToLower(line)
	// Example:
	// "ERROR codex_core::rollout::list: state db missing rollout path for thread ..."
	if strings.Contains(s, "codex_core::rollout::list") && strings.Contains(s, "missing rollout path") {
		return true
	}
	return false
}

func (hh *handleExec) emit(typ core.EventType, text string) {
	select {
	case hh.events <- core.Event{Type: typ, Text: text, Time: time.Now()}:
	default:
		// Drop on overflow: telegram side also batches.
	}
}

func (hh *handleExec) appendTranscript(s string) {
	base := hh.logDir
	_ = os.MkdirAll(filepath.Join(base, "sessions"), 0o755)
	logPath := filepath.Join(base, "sessions", hh.sessionID+".log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(s)
}

type codexJSON struct {
	Type     string     `json:"type"`
	ThreadID string     `json:"thread_id"`
	Item     *codexItem `json:"item"`
}

type codexItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
