package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"mybot/internal/core"
)

type Adapter struct {
	cmd    string
	args   []string
	dir    string
	logDir string

	fixed []string

	mode             string // "exec" or "interactive"
	skipGitRepoCheck bool

	statePath string

	stateMu sync.Mutex
	threads map[string]string // chat_id -> codex thread_id
}

func New(cmd string, args []string, dir string, logDir string) *Adapter {
	if strings.TrimSpace(logDir) == "" {
		logDir = "logs"
	}

	mode := strings.ToLower(strings.TrimSpace(os.Getenv("CODEX_DRIVER")))
	if mode == "" {
		if filepath.Base(cmd) == "codex" {
			mode = "exec"
		} else {
			mode = "interactive"
		}
	}
	if mode != "exec" && mode != "interactive" {
		mode = "interactive"
	}

	skipGit := envBool("CODEX_SKIP_GIT_REPO_CHECK", filepath.Base(cmd) == "codex")
	enableSearch := envBool("CODEX_ENABLE_SEARCH", false)

	fixed := detectFixedArgs(cmd)
	merged := make([]string, 0, len(fixed)+len(args))
	merged = append(merged, fixed...)
	merged = append(merged, args...)

	if dir != "" && filepath.Base(cmd) == "codex" && !hasCdFlag(merged) {
		merged = append([]string{"--cd", dir}, merged...)
	}
	if enableSearch && filepath.Base(cmd) == "codex" && !hasFlag(merged, "--search") {
		merged = append([]string{"--search"}, merged...)
	}

	a := &Adapter{
		cmd:              cmd,
		args:             merged,
		dir:              dir,
		logDir:           logDir,
		fixed:            fixed,
		mode:             mode,
		skipGitRepoCheck: skipGit,
		statePath:        filepath.Join(logDir, "state.json"),
		threads:          map[string]string{},
	}
	a.loadState()
	return a
}

type handle struct {
	sessionID string
	logDir    string

	cmd       *exec.Cmd
	pty       *os.File
	useProcPG bool

	stdin io.WriteCloser

	events chan core.Event
	once   sync.Once

	wmu sync.Mutex
}

func (h *handle) SessionID() string { return h.sessionID }

func (a *Adapter) Start(ctx context.Context, sessionID string) (core.Handle, error) {
	if a.mode == "exec" && filepath.Base(a.cmd) == "codex" {
		return a.startExec(ctx, sessionID)
	}
	return a.startInteractive(ctx, sessionID)
}

func (a *Adapter) startInteractive(ctx context.Context, sessionID string) (core.Handle, error) {
	if strings.TrimSpace(a.cmd) == "" {
		return nil, errors.New("empty adapter cmd")
	}

	// PTY makes interactive CLIs behave like a terminal.
	// If PTY is not permitted (EPERM) we fall back to pipes. Important:
	// pty.Start may partially populate cmd.Stdin/Stdout even when returning an error,
	// so we must not reuse that cmd instance for the pipe fallback.
	cmdPTY := a.newCmd(ctx, false)
	f, err := pty.Start(cmdPTY)
	ptyMode := true
	var stdin io.WriteCloser
	var stdout io.ReadCloser
	var stderr io.ReadCloser
	if err != nil {
		ptyErr := err
		// Some environments disallow PTYs (EPERM). Fall back to pipes so local testing still works.
		ptyMode = false

		cmdPipe := a.newCmd(ctx, true)
		stdin, err = cmdPipe.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("pty.Start: %v; StdinPipe: %w", ptyErr, err)
		}
		stdout, err = cmdPipe.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("pty.Start: %v; StdoutPipe: %w", ptyErr, err)
		}
		stderr, err = cmdPipe.StderrPipe()
		if err != nil {
			return nil, fmt.Errorf("pty.Start: %v; StderrPipe: %w", ptyErr, err)
		}
		if err := cmdPipe.Start(); err != nil {
			return nil, fmt.Errorf("pty.Start: %v; Start: %w", ptyErr, err)
		}
		cmdPTY = cmdPipe
	} else {
		stdin = f
		// Give the child a reasonable terminal size.
		_ = pty.Setsize(f, &pty.Winsize{Rows: 40, Cols: 120})
	}

	h := &handle{
		sessionID: sessionID,
		logDir:    a.logDir,
		cmd:       cmdPTY,
		pty:       f,
		useProcPG: !ptyMode,
		stdin:     stdin,
		events:    make(chan core.Event, 256),
	}

	if ptyMode {
		go h.readLoopPTY()
	} else {
		go h.readLoopPipe(stdout, core.EventStdout)
		go h.readLoopPipe(stderr, core.EventStderr)
	}
	go h.waitLoop()

	mode := "pty"
	if !ptyMode {
		mode = "pipes"
	}
	h.events <- core.Event{Type: core.EventStatus, Text: fmt.Sprintf("started pid=%d mode=%s cmd=%s", h.cmd.Process.Pid, mode, a.cmd), Time: time.Now()}
	return h, nil
}

func (a *Adapter) newCmd(ctx context.Context, setpgid bool) *exec.Cmd {
	cmd := exec.CommandContext(ctx, a.cmd, a.args...)
	if a.dir != "" {
		cmd.Dir = a.dir
	}
	cmd.Env = append(os.Environ(),
		// Widely-supported conventions to disable ANSI colors/spinners in CLI output.
		"NO_COLOR=1",
		"CLICOLOR=0",
		"FORCE_COLOR=0",
	)
	if setpgid {
		// Put in its own process group so we can send signals (Ctrl+C) to the whole tree.
		// Note: when using PTY, pty.Start/forkpty may already create a new session; forcing Setpgid can fail on macOS.
		setSysProcAttr(cmd)
	}
	return cmd
}

func detectFixedArgs(cmd string) []string {
	// Goal: choose safe "quality of life" flags when they exist, without hard-coding
	// a specific codex CLI version.
	help := strings.ToLower(readHelp(cmd))
	if help == "" {
		return nil
	}

	var out []string

	// Color / ANSI disabling. Prefer exact flags if they appear in help.
	// We keep this conservative: don't add "yes"/"auto-approve" style flags.
	if strings.Contains(help, "--no-color") {
		out = append(out, "--no-color")
	} else if strings.Contains(help, "--color") && strings.Contains(help, "never") {
		// Common pattern: --color=auto|always|never
		out = append(out, "--color=never")
	}

	if strings.Contains(help, "--no-ansi") {
		out = append(out, "--no-ansi")
	} else if strings.Contains(help, "--ansi") && strings.Contains(help, "never") {
		out = append(out, "--ansi=never")
	}

	// Progress/spinner suppression if available.
	if strings.Contains(help, "--no-progress") {
		out = append(out, "--no-progress")
	}

	// De-dup while preserving order.
	seen := map[string]struct{}{}
	j := 0
	for _, a := range out {
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		out[j] = a
		j++
	}
	return out[:j]
}

func readHelp(cmd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := exec.CommandContext(ctx, cmd, "--help")
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	_ = c.Run()
	s := stdout.String() + "\n" + stderr.String()

	// Trim to reduce cost and avoid weird binaries dumping too much.
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	if len(s) > 8000 {
		s = s[:8000]
	}
	return s
}

func hasCdFlag(args []string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == "-C" || args[i] == "--cd" {
			return true
		}
	}
	return false
}

func hasFlag(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return def
	}
}

func parseChatKey(sessionID string) (chatKey string, fresh bool) {
	// Expected format from SessionManager:
	// - "chat-<chatID>-<ts>"
	// - "chat-<chatID>-<ts>-fresh"
	if strings.HasPrefix(sessionID, "chat-") {
		rest := strings.TrimPrefix(sessionID, "chat-")
		parts := strings.Split(rest, "-")
		if len(parts) >= 2 && parts[0] != "" {
			chatKey = parts[0]
		}
	}
	if strings.HasSuffix(sessionID, "-fresh") {
		fresh = true
	}
	return chatKey, fresh
}

type stateFile struct {
	Threads map[string]string `json:"threads"`
}

func (a *Adapter) loadState() {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()

	b, err := os.ReadFile(a.statePath)
	if err != nil {
		return
	}
	var st stateFile
	if err := json.Unmarshal(b, &st); err != nil {
		return
	}
	if st.Threads != nil {
		a.threads = st.Threads
	}
}

func (a *Adapter) saveStateLocked() {
	st := stateFile{Threads: a.threads}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	tmp := a.statePath + ".tmp"
	_ = os.MkdirAll(filepath.Dir(a.statePath), 0o755)
	_ = os.WriteFile(tmp, b, 0o644)
	_ = os.Rename(tmp, a.statePath)
}

func (a *Adapter) getThread(chatKey string) string {
	if chatKey == "" {
		return ""
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.threads[chatKey]
}

func (a *Adapter) setThread(chatKey, threadID string) {
	if chatKey == "" || threadID == "" {
		return
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.threads == nil {
		a.threads = map[string]string{}
	}
	if a.threads[chatKey] == threadID {
		return
	}
	a.threads[chatKey] = threadID
	a.saveStateLocked()
}

func (a *Adapter) clearThread(chatKey string) {
	if chatKey == "" {
		return
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if _, ok := a.threads[chatKey]; !ok {
		return
	}
	delete(a.threads, chatKey)
	a.saveStateLocked()
}

func (a *Adapter) Events(h core.Handle) <-chan core.Event {
	switch hh := h.(type) {
	case *handleExec:
		return a.eventsExec(hh)
	case *handle:
		return hh.events
	default:
		return nil
	}
}

func (a *Adapter) Send(h core.Handle, input string) error {
	switch hh := h.(type) {
	case *handleExec:
		return a.sendExec(hh, input)
	case *handle:
		if hh.stdin == nil {
			return errors.New("session not started")
		}
		// Ensure a newline to submit input for most interactive CLIs.
		if !strings.HasSuffix(input, "\n") {
			input += "\n"
		}
		hh.wmu.Lock()
		defer hh.wmu.Unlock()
		_, err := io.WriteString(hh.stdin, input)
		return err
	default:
		return errors.New("unknown handle type")
	}
}

func (a *Adapter) Stop(h core.Handle) error {
	switch hh := h.(type) {
	case *handleExec:
		return a.stopExec(hh)
	case *handle:
		if hh.cmd == nil || hh.cmd.Process == nil {
			return nil
		}
		if hh.useProcPG {
			err := killProcessGroup(hh.cmd.Process.Pid)
			if err == nil {
				return nil
			}
		}
		return hh.cmd.Process.Signal(os.Interrupt)
	default:
		return nil
	}
}

func (h *handle) closeEvents() {
	h.once.Do(func() {
		close(h.events)
	})
}

func (h *handle) waitLoop() {
	err := h.cmd.Wait()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if status, ok := ee.Sys().(syscall.WaitStatus); ok {
				code = status.ExitStatus()
			} else {
				code = 1
			}
		} else {
			code = 1
		}
	}
	h.events <- core.Event{Type: core.EventExit, Code: code, Text: "process exited", Time: time.Now()}
	if h.pty != nil {
		_ = h.pty.Close()
	}
	if h.stdin != nil && h.stdin != h.pty {
		_ = h.stdin.Close()
	}
	h.closeEvents()
}

func (h *handle) readLoopPTY() {
	defer func() {
		// If the PTY read ends before waitLoop, still ensure we don't leak the file.
		if h.pty != nil {
			_ = h.pty.Close()
		}
	}()

	// Also tee to a session transcript on disk for debugging.
	base := h.logDir
	_ = os.MkdirAll(filepath.Join(base, "sessions"), 0o755)
	logPath := filepath.Join(base, "sessions", h.sessionID+".log")
	lf, _ := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	defer func() {
		if lf != nil {
			_ = lf.Close()
		}
	}()

	// Codex (and other TUIs) may query the terminal for cursor position using DSR (ESC [ 6 n).
	// A PTY is not a real terminal emulator and won't auto-respond, so we fake a minimal response.
	// This is enough for many CLIs to start without failing.
	buf := make([]byte, 4096)
	pending := make([]byte, 0, 8192)

	for {
		n, err := h.pty.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)

			// Strip DSR queries from output and respond on stdin.
			pending = h.handleTermQueries(pending)

			if len(pending) > 0 {
				s := string(pending)
				pending = pending[:0]
				if lf != nil {
					_, _ = lf.WriteString(s)
				}
				h.events <- core.Event{Type: core.EventStdout, Text: s, Time: time.Now()}
			}
		}
		if err != nil {
			if err == io.EOF {
				return
			}
			h.events <- core.Event{Type: core.EventStderr, Text: fmt.Sprintf("read error: %v\n", err), Time: time.Now()}
			return
		}
	}
}

func (h *handle) readLoopPipe(r io.ReadCloser, typ core.EventType) {
	defer func() {
		if r != nil {
			_ = r.Close()
		}
	}()
	if r == nil {
		return
	}

	// Also tee to a session transcript on disk for debugging.
	base := h.logDir
	_ = os.MkdirAll(filepath.Join(base, "sessions"), 0o755)
	logPath := filepath.Join(base, "sessions", h.sessionID+".log")
	lf, _ := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	defer func() {
		if lf != nil {
			_ = lf.Close()
		}
	}()

	sc := bufio.NewScanner(r)
	// Allow bigger lines than default 64K.
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		line := sc.Text() + "\n"
		if lf != nil {
			_, _ = lf.WriteString(line)
		}
		h.events <- core.Event{Type: typ, Text: line, Time: time.Now()}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		h.events <- core.Event{Type: core.EventStderr, Text: fmt.Sprintf("read error: %v\n", err), Time: time.Now()}
	}
}

var (
	dsrCursor  = []byte{0x1b, '[', '6', 'n'}      // ESC [ 6 n
	dsrCursorQ = []byte{0x1b, '[', '?', '6', 'n'} // ESC [ ? 6 n
)

func (h *handle) handleTermQueries(b []byte) []byte {
	// Replace all cursor position queries with a canned response.
	for {
		i := bytes.Index(b, dsrCursor)
		j := bytes.Index(b, dsrCursorQ)
		if i == -1 && j == -1 {
			return b
		}
		// Pick earliest match.
		k := i
		n := len(dsrCursor)
		if k == -1 || (j != -1 && j < k) {
			k = j
			n = len(dsrCursorQ)
		}

		// Remove the query from output.
		b = append(b[:k], b[k+n:]...)

		// Respond on stdin: row=1 col=1 (ESC [ 1 ; 1 R)
		h.wmu.Lock()
		if h.stdin != nil {
			_, _ = h.stdin.Write([]byte{0x1b, '[', '1', ';', '1', 'R'})
		}
		h.wmu.Unlock()
	}
}
