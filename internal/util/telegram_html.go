package util

import (
	"html"
	"regexp"
	"strings"
)

var (
	listRE = regexp.MustCompile(`(?m)^\s*[-*]\s+`)
)

// FormatTelegramHTML returns an HTML-safe message body and whether it should be sent as HTML.
// We keep this conservative: plain text becomes escaped HTML, and code/list-like output becomes <pre>.
func FormatTelegramHTML(s string) (string, bool) {
	s = SanitizeTelegramText(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")

	looksLikeBlock := strings.Contains(s, "```") || strings.Contains(s, "\n") || listRE.MatchString(s)
	// Improve list readability: "- x" -> "• x"
	if listRE.MatchString(s) {
		s = listRE.ReplaceAllString(s, "• ")
	}

	esc := html.EscapeString(s)
	if looksLikeBlock {
		return "<pre>" + esc + "</pre>", true
	}
	return esc, true
}
