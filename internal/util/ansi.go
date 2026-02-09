package util

import "regexp"

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// StripANSI removes common ANSI escape sequences from terminal output.
func StripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}
