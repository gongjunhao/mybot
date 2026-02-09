package telegram

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type dailyNL struct {
	HHMM   string
	Prompt string
}

var dailyRE = regexp.MustCompile(`^\s*每天(?:(上午下午|早上晚上|早晚|上午|下午|早上|晚上)\s*)?(\d{1,2})\s*点(?:(半)|\s*(\d{1,2})\s*分?)?\s*(.+?)\s*$`)

func parseDailySchedules(text string) ([]dailyNL, bool) {
	m := dailyRE.FindStringSubmatch(text)
	if m == nil {
		return nil, false
	}
	period := strings.TrimSpace(m[1])
	hourS := m[2]
	half := m[3]
	minS := m[4]
	prompt := strings.TrimSpace(m[5])
	if prompt == "" {
		return nil, false
	}

	h, err := strconv.Atoi(hourS)
	if err != nil || h < 0 || h > 23 {
		return nil, false
	}
	min := 0
	if half != "" {
		min = 30
	} else if strings.TrimSpace(minS) != "" {
		mm, err := strconv.Atoi(strings.TrimSpace(minS))
		if err != nil || mm < 0 || mm > 59 {
			return nil, false
		}
		min = mm
	}

	applyPeriod := func(p string, hour int) int {
		switch p {
		case "下午", "晚上":
			if hour < 12 {
				return hour + 12
			}
			return hour
		case "上午", "早上":
			return hour
		default:
			return hour
		}
	}

	periods := []string{period}
	switch period {
	case "上午下午":
		periods = []string{"上午", "下午"}
	case "早上晚上", "早晚":
		periods = []string{"早上", "晚上"}
	case "":
		periods = []string{""}
	}

	seen := map[string]bool{}
	var out []dailyNL
	for _, p := range periods {
		hh := applyPeriod(p, h)
		hhmm := fmt.Sprintf("%02d:%02d", hh, min)
		if seen[hhmm] {
			continue
		}
		seen[hhmm] = true
		out = append(out, dailyNL{HHMM: hhmm, Prompt: prompt})
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func parseDailySchedule(text string) (dailyNL, bool) {
	ts, ok := parseDailySchedules(text)
	if !ok || len(ts) != 1 {
		return dailyNL{}, false
	}
	return ts[0], true
}

func defaultAINewsPrompt(raw string) string {
	// Keep the user's intent but make the output deterministic and usable.
	return "请获取并整理【最新 AI 资讯】，要求：\n" +
		"1) 中文输出，5-10 条要点\n" +
		"2) 优先最近 24 小时（如果做不到，标注日期）\n" +
		"3) 每条包含：标题 + 1-2 句摘要 + 来源（可复制的链接或媒体名）\n" +
		"4) 末尾给一个 3-5 行的今日趋势总结\n" +
		"\n用户原始需求：" + raw
}
