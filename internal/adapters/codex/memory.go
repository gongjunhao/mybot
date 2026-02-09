package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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

	// Last suggestions extracted during compaction (for user visibility).
	SkillIdeas []string `json:"skill_ideas"`
}

type compactResult struct {
	Summary    string   `json:"summary"`
	Rules      []string `json:"durable_rules"`
	Prefs      []string `json:"user_prefs"`
	SkillIdeas []string `json:"skill_ideas"`
}

func (a *Adapter) memoryEnabled() bool {
	return envBool("MEMORY_ENABLE", true) && filepath.Base(a.cmd) == "codex" && a.mode == "exec"
}

func (a *Adapter) memoryTokenThreshold() int {
	return envInt("MEMORY_TOKEN_THRESHOLD", 60000)
}

func (a *Adapter) memoryTurnThreshold() int {
	return envInt("MEMORY_TURN_THRESHOLD", 40)
}

func (a *Adapter) memoryPath() string {
	return filepath.Join(a.logDir, "memory.json")
}

func (a *Adapter) loadMemory() {
	a.memMu.Lock()
	defer a.memMu.Unlock()

	b, err := os.ReadFile(a.memoryPath())
	if err != nil {
		return
	}
	var f memoryFile
	if err := json.Unmarshal(b, &f); err != nil {
		return
	}
	if f.Chats == nil {
		return
	}
	a.mem = f.Chats
}

func (a *Adapter) saveMemoryLocked() {
	f := memoryFile{Chats: a.mem}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	tmp := a.memoryPath() + ".tmp"
	_ = os.MkdirAll(filepath.Dir(a.memoryPath()), 0o755)
	_ = os.WriteFile(tmp, b, 0o644)
	_ = os.Rename(tmp, a.memoryPath())
}

func (a *Adapter) getMem(chatKey string) *chatMemory {
	if chatKey == "" {
		return nil
	}
	a.memMu.Lock()
	defer a.memMu.Unlock()
	if a.mem == nil {
		a.mem = map[string]*chatMemory{}
	}
	m := a.mem[chatKey]
	if m == nil {
		m = &chatMemory{}
		a.mem[chatKey] = m
	}
	return m
}

func (a *Adapter) memoryPrefix(chatKey string) string {
	if !a.memoryEnabled() {
		return ""
	}
	m := a.getMem(chatKey)
	if m == nil {
		return ""
	}
	var b strings.Builder
	if len(m.Rules) > 0 {
		b.WriteString("持久记忆规则（长期生效，优先遵守）：\n")
		for _, r := range m.Rules {
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
	if m.Summary != "" {
		b.WriteString("对话摘要（用于续聊）：\n")
		b.WriteString(m.Summary)
		b.WriteString("\n\n")
	}
	if len(m.Prefs) > 0 {
		b.WriteString("偏好（尽量遵守）：\n")
		for _, p := range m.Prefs {
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
	if b.Len() == 0 {
		return ""
	}
	return b.String()
}

func (a *Adapter) onTurnCompleted(chatKey, threadID string, usage codexUsage, notify func(string)) {
	if !a.memoryEnabled() || chatKey == "" {
		return
	}
	m := a.getMem(chatKey)
	if m == nil {
		return
	}

	a.memMu.Lock()
	m.TurnsSinceCompact++
	m.TokensSinceCompact += usage.InputTokens + usage.OutputTokens
	m.UpdatedAt = time.Now()
	need := (m.TokensSinceCompact >= a.memoryTokenThreshold()) || (m.TurnsSinceCompact >= a.memoryTurnThreshold())
	tokensNow := m.TokensSinceCompact
	turnsNow := m.TurnsSinceCompact
	a.saveMemoryLocked()
	a.memMu.Unlock()

	if !need || threadID == "" {
		return
	}

	// Avoid parallel compactions per chat.
	a.compactMu.Lock()
	if a.compacting == nil {
		a.compacting = map[string]bool{}
	}
	if a.compacting[chatKey] {
		a.compactMu.Unlock()
		return
	}
	a.compacting[chatKey] = true
	a.compactMu.Unlock()

	go func() {
		if notify != nil {
			notify(fmt.Sprintf("已达到压缩阈值（tokens=%d turns=%d），开始整理对话摘要与持久记忆...\n", tokensNow, turnsNow))
		}
		defer func() {
			a.compactMu.Lock()
			a.compacting[chatKey] = false
			a.compactMu.Unlock()
		}()

		if err := a.compactChat(chatKey, threadID); err != nil {
			if notify != nil {
				notify("对话压缩失败（将继续使用原会话）： " + err.Error() + "\n")
			}
			// Best-effort: keep running; errors will surface in transcripts.
			return
		}
		if notify != nil {
			mem := a.getMem(chatKey)
			rn := 0
			var ideas []string
			if mem != nil {
				rn = len(mem.Rules)
				ideas = mem.SkillIdeas
			}
			msg := fmt.Sprintf("已完成对话压缩：已生成摘要；持久规则 %d 条。", rn)
			if len(ideas) > 0 {
				msg += "\n可考虑沉淀为 skills 的方向：\n"
				for _, it := range ideas {
					it = strings.TrimSpace(it)
					if it == "" {
						continue
					}
					msg += "- " + it + "\n"
				}
			}
			notify(msg)
		}
	}()
}

func (a *Adapter) compactChat(chatKey, threadID string) error {
	// Ask codex to summarize and extract durable rules as JSON.
	prompt := "请你把我们到目前为止的对话内容进行“压缩整理”，并输出严格 JSON（不要 markdown，不要代码块），格式：\n" +
		"{\n" +
		"  \"summary\": \"...（200-600字中文摘要）...\",\n" +
		"  \"durable_rules\": [\"...长期规则...\"],\n" +
		"  \"user_prefs\": [\"...偏好...\"],\n" +
		"  \"skill_ideas\": [\"...可考虑沉淀为 skill 的提示/流程（可选）...\"]\n" +
		"}\n" +
		"要求：\n" +
		"- summary 聚焦事实、结论、待办\n" +
		"- durable_rules 只保留用户明确、多次强调、长期适用的规则（最多 20 条）\n" +
		"- user_prefs 只保留写作/格式/交互偏好（最多 20 条）\n" +
		"- skill_ideas 给出 0-5 条可升级沉淀的方向\n"

	text, err := a.runCodexResumeJSON(threadID, prompt)
	if err != nil {
		return err
	}

	var res compactResult
	if err := json.Unmarshal([]byte(extractJSON(text)), &res); err != nil {
		// If parsing fails, still store raw as summary.
		res.Summary = strings.TrimSpace(text)
	}

	a.memMu.Lock()
	if a.mem == nil {
		a.mem = map[string]*chatMemory{}
	}
	m := a.mem[chatKey]
	if m == nil {
		m = &chatMemory{}
		a.mem[chatKey] = m
	}
	// Merge rules/prefs with de-dup.
	m.Summary = strings.TrimSpace(res.Summary)
	m.Rules = mergeUnique(m.Rules, res.Rules, 20)
	m.Prefs = mergeUnique(m.Prefs, res.Prefs, 20)
	m.SkillIdeas = trimList(res.SkillIdeas, 5)
	m.CompactedAt = time.Now()
	m.UpdatedAt = time.Now()
	m.TokensSinceCompact = 0
	m.TurnsSinceCompact = 0
	a.saveMemoryLocked()
	a.memMu.Unlock()

	// Start a new codex thread next time by clearing persisted thread_id.
	a.dropThread(chatKey)
	return nil
}

func (a *Adapter) runCodexResumeJSON(threadID string, prompt string) (string, error) {
	if threadID == "" {
		return "", errors.New("empty threadID")
	}

	argv := make([]string, 0, len(a.args)+8)
	argv = append(argv, a.args...)
	argv = append(argv, "exec", "resume", "--json")
	if a.skipGitRepoCheck {
		argv = append(argv, "--skip-git-repo-check")
	}
	argv = append(argv, threadID, prompt)

	cmd := exec.CommandContext(context.Background(), a.cmd, argv...)
	setSysProcAttr(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("compact run failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}

	// Parse JSONL and return the last agent_message text.
	var last string
	sc := bufioNewScanner(&stdout)
	for sc.Scan() {
		var ev codexJSON
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Type == "item.completed" && ev.Item != nil && ev.Item.Type == "agent_message" {
			last = ev.Item.Text
		}
	}
	if strings.TrimSpace(last) == "" {
		// Fallback to raw stdout.
		return stdout.String(), nil
	}
	return last, nil
}

func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}

func mergeUnique(base []string, add []string, limit int) []string {
	seen := map[string]bool{}
	var out []string
	push := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range base {
		push(s)
	}
	for _, s := range add {
		push(s)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func trimList(in []string, limit int) []string {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func bufioNewScanner(r *bytes.Buffer) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return sc
}
