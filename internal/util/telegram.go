package util

import "strings"

// SanitizeTelegramText removes NUL bytes.
func SanitizeTelegramText(s string) string {
	return strings.ReplaceAll(s, "\x00", "")
}

// TrimToBytes truncates a string to at most n bytes (not runes).
// Fine for MVP since CLI output is typically ASCII/UTF-8; Telegram accepts UTF-8.
func TrimToBytes(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}
