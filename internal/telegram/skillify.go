package telegram

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
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"mybot/internal/config"
	"mybot/internal/util"
)

func handleSkillifyCmd(ctx context.Context, bot *tgbotapi.BotAPI, cfg config.Config, chatID int64, cmd []string) {
	if len(cmd) < 2 {
		sendText(bot, chatID, "usage: /skillify <name> <ideaIndex>\n例：/skillify ai-news 1\n先用 /memory ideas 查看 ideaIndex")
		return
	}
	name := util.SafeFilename(cmd[1])
	if name == "" {
		sendText(bot, chatID, "skillify: bad name")
		return
	}
	ideaIdx := 0
	if len(cmd) >= 3 {
		n, err := strconv.Atoi(cmd[2])
		if err != nil || n <= 0 {
			sendText(bot, chatID, "skillify: ideaIndex must be positive integer")
			return
		}
		ideaIdx = n
	} else {
		sendText(bot, chatID, "usage: /skillify <name> <ideaIndex>\n例：/skillify ai-news 1\n先用 /memory ideas 查看 ideaIndex")
		return
	}

	ms := NewMemoryStore(cfg)
	mem, err := ms.Get(chatID)
	if err != nil {
		sendText(bot, chatID, fmt.Sprintf("skillify: %v", err))
		return
	}
	if mem == nil || len(mem.SkillIdeas) == 0 {
		sendText(bot, chatID, "skillify: no ideas; try /memory ideas")
		return
	}
	if ideaIdx < 1 || ideaIdx > len(mem.SkillIdeas) {
		sendText(bot, chatID, fmt.Sprintf("skillify: ideaIndex out of range (1..%d)", len(mem.SkillIdeas)))
		return
	}
	idea := strings.TrimSpace(mem.SkillIdeas[ideaIdx-1])
	if idea == "" {
		sendText(bot, chatID, "skillify: empty idea")
		return
	}

	root := skillsRoot(cfg)
	if root == "" {
		sendText(bot, chatID, "skillify: SKILLS_DIR not configured")
		return
	}
	dstDir := filepath.Join(root, name)
	dstFile := filepath.Join(dstDir, "SKILL.md")
	_ = os.MkdirAll(dstDir, 0o755)

	var existing string
	if b, err := os.ReadFile(dstFile); err == nil {
		existing = string(b)
		if len(existing) > 8000 {
			existing = existing[:8000]
		}
	}

	prompt := buildSkillifyPrompt(name, idea, mem, existing)
	sendText(bot, chatID, fmt.Sprintf("skillify: generating %s ...", name))

	md, err := runCodexOnce(ctx, cfg, prompt)
	if err != nil {
		sendText(bot, chatID, fmt.Sprintf("skillify failed: %v", err))
		return
	}
	md = strings.TrimSpace(stripCodeFence(md))
	if md == "" {
		sendText(bot, chatID, "skillify failed: empty output")
		return
	}

	if err := os.WriteFile(dstFile, []byte(md+"\n"), 0o644); err != nil {
		sendText(bot, chatID, fmt.Sprintf("skillify write failed: %v", err))
		return
	}

	sendText(bot, chatID, fmt.Sprintf("installed/updated skill: %s (SKILL.md)\npath: %s", name, dstFile))
}

func buildSkillifyPrompt(name string, idea string, mem *chatMemory, existing string) string {
	var b strings.Builder
	b.WriteString("你是一个“Codex Skill 作者”。请为我生成一个可直接使用的 SKILL.md（中文），用于 Codex skills。\n")
	b.WriteString("skill 名称（文件夹名）: " + name + "\n")
	b.WriteString("目标/方向: " + idea + "\n\n")

	if mem != nil {
		if mem.Summary != "" {
			b.WriteString("背景摘要（供参考，不要原样照抄）:\n")
			b.WriteString(mem.Summary)
			b.WriteString("\n\n")
		}
		if len(mem.Rules) > 0 {
			b.WriteString("用户长期规则（必须遵守）:\n")
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
			b.WriteString("用户偏好（尽量遵守）:\n")
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
	}

	if strings.TrimSpace(existing) != "" {
		b.WriteString("现有 SKILL.md（请在此基础上升级，保持兼容并改进；不要删除有价值的内容）：\n")
		b.WriteString("-----BEGIN EXISTING-----\n")
		b.WriteString(existing)
		b.WriteString("\n-----END EXISTING-----\n\n")
		b.WriteString("请输出“升级后的完整 SKILL.md”。\n")
	} else {
		b.WriteString("请输出“完整 SKILL.md”。\n")
	}

	b.WriteString("硬性要求：\n")
	b.WriteString("- 只输出 SKILL.md 内容本身，不要额外解释，不要代码块围栏\n")
	b.WriteString("- 包含：用途/触发规则/工作流/安全注意/示例（至少 3 个）\n")
	b.WriteString("- 对于可执行的操作要明确步骤和约束（比如禁止破坏性命令、需要确认等）\n")
	b.WriteString("- 保持内容可长期维护；必要时加入“版本/更新记录”小节\n")
	return b.String()
}

func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// remove first fence line
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSpace(s)
	}
	if j := strings.LastIndex(s, "```"); j >= 0 {
		s = strings.TrimSpace(s[:j])
	}
	return s
}

func runCodexOnce(ctx context.Context, cfg config.Config, prompt string) (string, error) {
	cmdPath := cfg.CodexCmd
	if cmdPath == "" {
		cmdPath = "codex"
	}
	args := append([]string{}, cfg.CodexArgs...)
	if cfg.WorkDir != "" && !hasCdFlagLocal(args) {
		args = append([]string{"--cd", cfg.WorkDir}, args...)
	}
	if strings.TrimSpace(os.Getenv("CODEX_ENABLE_SEARCH")) == "1" && !hasFlagLocal(args, "--search") && filepath.Base(cmdPath) == "codex" {
		args = append([]string{"--search"}, args...)
	}

	argv := make([]string, 0, len(args)+8)
	argv = append(argv, args...)
	argv = append(argv, "exec", "--json", "--skip-git-repo-check", prompt)

	c := exec.CommandContext(ctx, cmdPath, argv...)
	util.SetProcessGroup(c)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("codex exec failed: %s", msg)
	}

	// Parse JSONL and return last agent message text.
	last := ""
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var ev struct {
			Type string `json:"type"`
			Item *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Type == "item.completed" && ev.Item != nil && ev.Item.Type == "agent_message" {
			last = ev.Item.Text
		}
	}
	if strings.TrimSpace(last) == "" {
		out := strings.TrimSpace(stdout.String())
		if out == "" {
			return "", errors.New("empty codex output")
		}
		return out, nil
	}
	return last, nil
}

func hasCdFlagLocal(args []string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == "-C" || args[i] == "--cd" {
			return true
		}
	}
	return false
}

func hasFlagLocal(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
