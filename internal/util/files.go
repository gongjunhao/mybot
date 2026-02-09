package util

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var safeNameRE = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func SafeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" {
		return "file"
	}
	name = safeNameRE.ReplaceAllString(name, "_")
	if name == "" {
		return "file"
	}
	return name
}

func UniqueUploadName(original string) string {
	base := SafeFilename(original)
	return time.Now().Format("20060102_150405") + "_" + base
}
