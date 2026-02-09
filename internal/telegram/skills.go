package telegram

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"mybot/internal/config"
	"mybot/internal/util"
)

func skillsRoot(cfg config.Config) string {
	return cfg.SkillsDir
}

func listSkills(cfg config.Config) ([]string, error) {
	root := skillsRoot(cfg)
	if root == "" {
		return nil, errors.New("SKILLS_DIR not set and default could not be resolved")
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func removeSkill(cfg config.Config, name string) (string, error) {
	root := skillsRoot(cfg)
	if root == "" {
		return "", errors.New("skills dir not configured")
	}
	name = util.SafeFilename(name)
	if name == "" {
		return "", errors.New("bad skill name")
	}
	dst := filepath.Join(root, name)
	rootAbs, _ := filepath.Abs(root)
	dstAbs, _ := filepath.Abs(dst)
	if rootAbs != "" && !strings.HasPrefix(dstAbs, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing to delete outside skills dir: %s", dstAbs)
	}
	if err := os.RemoveAll(dstAbs); err != nil {
		return "", err
	}
	return name, nil
}

func installSkill(cfg config.Config, name, source string) (string, error) {
	root := skillsRoot(cfg)
	if root == "" {
		return "", errors.New("skills dir not configured")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}

	source = strings.TrimSpace(source)
	if source == "" {
		return "", errors.New("empty source")
	}
	if name == "" {
		name = deriveSkillName(source)
	}
	name = util.SafeFilename(name)
	if name == "" {
		return "", errors.New("bad skill name")
	}
	dst := filepath.Join(root, name)
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("already exists: %s", name)
	}

	// If source is a local directory, copy it. Otherwise, treat it as a git URL and clone.
	if fi, err := os.Stat(source); err == nil && fi.IsDir() {
		if err := copyDir(source, dst); err != nil {
			_ = os.RemoveAll(dst)
			return "", err
		}
	} else {
		if err := gitClone(source, dst); err != nil {
			_ = os.RemoveAll(dst)
			return "", err
		}
	}

	// Soft validation: SKILL.md should exist.
	if _, err := os.Stat(filepath.Join(dst, "SKILL.md")); err != nil {
		// Still install, but warn to user via caller.
		return name, fmt.Errorf("installed but missing SKILL.md at root: %s", name)
	}
	return name, nil
}

func deriveSkillName(source string) string {
	s := strings.TrimSpace(source)
	s = strings.TrimSuffix(s, "/")
	base := filepath.Base(s)
	base = strings.TrimSuffix(base, ".git")
	if base == "" || base == "." || base == "/" {
		return "skill"
	}
	return base
}

func gitClone(url, dst string) error {
	// depth=1 is fine for skills.
	cmd := exec.Command("git", "clone", "--depth", "1", "--", url, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func copyDir(src, dst string) error {
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)

	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("not a directory: %s", src)
	}
	if err := os.MkdirAll(dst, fi.Mode()&0o777); err != nil {
		return err
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range ents {
		name := e.Name()
		if name == ".git" {
			continue
		}
		s := filepath.Join(src, name)
		d := filepath.Join(dst, name)
		if e.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(s, d); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fi.Mode()&0o777)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
