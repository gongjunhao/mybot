package telegram

import "testing"

func TestParseDailySchedules_Single(t *testing.T) {
	ts, ok := parseDailySchedules("每天上午9点 提醒我打卡")
	if !ok {
		t.Fatalf("expected ok")
	}
	if len(ts) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(ts))
	}
	if ts[0].HHMM != "09:00" {
		t.Fatalf("expected HHMM=09:00, got %q", ts[0].HHMM)
	}
	if ts[0].Prompt != "提醒我打卡" {
		t.Fatalf("unexpected prompt: %q", ts[0].Prompt)
	}
}

func TestParseDailySchedules_PM(t *testing.T) {
	ts, ok := parseDailySchedules("每天下午6点32分，提醒我打卡")
	if !ok {
		t.Fatalf("expected ok")
	}
	if len(ts) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(ts))
	}
	if ts[0].HHMM != "18:32" {
		t.Fatalf("expected HHMM=18:32, got %q", ts[0].HHMM)
	}
}

func TestParseDailySchedules_AMPM(t *testing.T) {
	ts, ok := parseDailySchedules("每天上午下午6点32分，提醒我打卡")
	if !ok {
		t.Fatalf("expected ok")
	}
	if len(ts) != 2 {
		t.Fatalf("expected 2 schedules, got %d", len(ts))
	}
	if ts[0].HHMM != "06:32" || ts[1].HHMM != "18:32" {
		t.Fatalf("unexpected HHMMs: %q, %q", ts[0].HHMM, ts[1].HHMM)
	}
}

func TestParseDailySchedules_ZaoWan(t *testing.T) {
	ts, ok := parseDailySchedules("每天早晚6点半 提醒我打卡")
	if !ok {
		t.Fatalf("expected ok")
	}
	if len(ts) != 2 {
		t.Fatalf("expected 2 schedules, got %d", len(ts))
	}
	if ts[0].HHMM != "06:30" || ts[1].HHMM != "18:30" {
		t.Fatalf("unexpected HHMMs: %q, %q", ts[0].HHMM, ts[1].HHMM)
	}
}
