package util

import (
	"html"
	"regexp"
	"strings"
)

var (
	indentRE   = regexp.MustCompile(`(?m)^[ \t]{4,}`)
	shellRE    = regexp.MustCompile(`(?m)^.*\\$ `)
	codeFence  = regexp.MustCompile("```")
	bulletRE   = regexp.MustCompile(`^\s*([-*]|•)\s+`)
	urlPrefix1 = "http://"
	urlPrefix2 = "https://"
)

// FormatTelegramHTML formats content for Telegram HTML parse mode.
// Goal: look like "markdown preview" without forcing <pre> (which shows a "copy" affordance).
// We only use <pre> when the content strongly looks like code/shell output.
func FormatTelegramHTML(s string) (string, bool) {
	s = SanitizeTelegramText(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")

	if looksLikeCode(s) {
		esc := html.EscapeString(s)
		return "<pre>" + esc + "</pre>", true
	}

	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = formatLine(lines[i])
	}
	out := strings.Join(lines, "\n")
	return out, true
}

func looksLikeCode(s string) bool {
	if codeFence.MatchString(s) {
		return true
	}
	if indentRE.MatchString(s) {
		return true
	}
	if shellRE.MatchString(s) {
		return true
	}
	return false
}

func formatLine(line string) string {
	raw := strings.TrimRight(line, "\r")
	if bulletRE.MatchString(raw) {
		raw = bulletRE.ReplaceAllString(raw, "• ")
	}
	return renderInlineMarkdownToHTML(raw)
}

// renderInlineMarkdownToHTML supports a tiny subset:
// - **bold**
// - `inline code`
// - [text](https://url)
// Everything else is HTML-escaped.
func renderInlineMarkdownToHTML(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)

	inBold := false
	inCode := false

	closeAll := func() {
		if inCode {
			b.WriteString("</code>")
			inCode = false
		}
		if inBold {
			b.WriteString("</b>")
			inBold = false
		}
	}

	for i := 0; i < len(s); {
		// Auto-link plain URLs (outside code).
		if !inCode && (strings.HasPrefix(s[i:], urlPrefix1) || strings.HasPrefix(s[i:], urlPrefix2)) {
			u, n, suffix := readURL(s[i:])
			if u != "" && n > 0 {
				b.WriteString(`<a href="`)
				b.WriteString(html.EscapeString(u))
				b.WriteString(`">`)
				b.WriteString(html.EscapeString(u))
				b.WriteString(`</a>`)
				if suffix != "" {
					b.WriteString(html.EscapeString(suffix))
				}
				i += n
				continue
			}
		}

		// Link: [text](https://...)
		if s[i] == '[' && !inCode {
			if txt, url, n, ok := tryParseLink(s[i:]); ok {
				// Don't allow nested markup in link text; treat as plain.
				b.WriteString(`<a href="`)
				b.WriteString(html.EscapeString(url))
				b.WriteString(`">`)
				b.WriteString(html.EscapeString(txt))
				b.WriteString(`</a>`)
				i += n
				continue
			}
		}

		// Bold toggle: **
		if !inCode && i+1 < len(s) && s[i] == '*' && s[i+1] == '*' {
			if inBold {
				b.WriteString("</b>")
				inBold = false
			} else {
				b.WriteString("<b>")
				inBold = true
			}
			i += 2
			continue
		}

		// Code toggle: `
		if s[i] == '`' {
			// Code should win over bold visually.
			if inCode {
				b.WriteString("</code>")
				inCode = false
			} else {
				if inBold {
					b.WriteString("</b>")
					inBold = false
				}
				b.WriteString("<code>")
				inCode = true
			}
			i++
			continue
		}

		// Regular char.
		b.WriteString(html.EscapeString(s[i : i+1]))
		i++
	}

	closeAll()
	return b.String()
}

func readURL(s string) (string, int, string) {
	// Read until whitespace; then trim common trailing punctuation.
	j := 0
	for j < len(s) {
		c := s[j]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			break
		}
		j++
	}
	raw := s[:j]
	cut := len(raw)
	for cut > 0 {
		last := raw[cut-1]
		switch last {
		case '.', ',', ';', ':', '!', '?', ')', ']', '}', '"', '\'':
			cut--
			continue
		default:
			goto done
		}
	}
done:
	u := raw[:cut]
	if u == "" {
		return "", 0, ""
	}
	return u, j, raw[cut:]
}

func tryParseLink(s string) (text string, url string, n int, ok bool) {
	// Expect: [text](url)
	// Very small parser; no nested brackets.
	i := strings.IndexByte(s, ']')
	if i <= 1 {
		return "", "", 0, false
	}
	if len(s) < i+3 || s[i+1] != '(' {
		return "", "", 0, false
	}
	j := strings.IndexByte(s[i+2:], ')')
	if j < 0 {
		return "", "", 0, false
	}
	txt := s[1:i]
	u := s[i+2 : i+2+j]
	u = strings.TrimSpace(u)
	if !(strings.HasPrefix(u, urlPrefix1) || strings.HasPrefix(u, urlPrefix2)) {
		return "", "", 0, false
	}
	return txt, u, i + 2 + j + 1, true
}
