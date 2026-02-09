package telegram

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mybot/internal/config"
	"mybot/internal/util"
)

func uploadsRoot(cfg config.Config) string {
	return filepath.Join(cfg.WorkDir, cfg.UploadDir)
}

func listUploads(root string, uploadDirName string, limit int) ([]string, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	type item struct {
		name string
		mod  time.Time
	}
	var items []item
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{name: e.Name(), mod: info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, filepath.ToSlash(filepath.Join(uploadDirName, items[i].name)))
	}
	return out, nil
}

func deleteUpload(cfg config.Config, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", errors.New("empty target")
	}
	root := uploadsRoot(cfg)
	rootAbs, _ := filepath.Abs(root)

	// If arg is a bare filename (no separators), resolve to the newest match.
	if !strings.ContainsAny(arg, `/\`) {
		// Try exact name and suffix match for original names: <timestamp>_<original>
		want := util.SafeFilename(arg)
		cand, err := newestMatch(root, func(name string) bool {
			return name == want || strings.HasSuffix(name, "_"+want)
		})
		if err != nil {
			return "", err
		}
		if cand == "" {
			return "", fmt.Errorf("not found in %s: %s", cfg.UploadDir, want)
		}
		arg = filepath.Join(root, cand)
	} else {
		// Treat as a path; if relative, resolve from WORKDIR.
		if !filepath.IsAbs(arg) {
			arg = filepath.Join(cfg.WorkDir, arg)
		}
	}

	targetAbs, err := filepath.Abs(filepath.Clean(arg))
	if err != nil {
		return "", err
	}
	// Constrain deletion to uploads root.
	if rootAbs != "" {
		if !strings.HasPrefix(targetAbs, rootAbs+string(os.PathSeparator)) && targetAbs != rootAbs {
			return "", fmt.Errorf("refusing to delete outside uploads dir: %s", targetAbs)
		}
	}

	if err := os.Remove(targetAbs); err != nil {
		return "", err
	}
	rel, _ := filepath.Rel(cfg.WorkDir, targetAbs)
	return filepath.ToSlash(rel), nil
}

func newestMatch(root string, ok func(name string) bool) (string, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var best string
	var bestTime time.Time
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !ok(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best = name
			bestTime = info.ModTime()
		}
	}
	return best, nil
}

func nlDeleteArg(text string) (string, bool) {
	t := strings.TrimSpace(text)
	for _, prefix := range []string{"删除这个", "删除此", "删除"} {
		if strings.HasPrefix(t, prefix) {
			arg := strings.TrimSpace(strings.TrimPrefix(t, prefix))
			if arg != "" {
				return arg, true
			}
		}
	}
	return "", false
}
