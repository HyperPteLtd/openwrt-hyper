package release

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var conventional = regexp.MustCompile(`^([a-z]+)(\([^)]*\))?(!)?:`)

// Bump returns the next version after latest for these conventional commit messages:
// a breaking change raises the major version, a feat the minor, anything else the patch.
func Bump(latest string, messages []string) (string, error) {
	m := versionPattern.FindStringSubmatch(latest)
	if m == nil {
		return "", fmt.Errorf("invalid release version %q", latest)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	level := 0
	for _, msg := range messages {
		subject, body, _ := strings.Cut(msg, "\n")
		c := conventional.FindStringSubmatch(subject)
		switch {
		case c != nil && c[3] == "!", strings.Contains(body, "BREAKING CHANGE:"), strings.Contains(body, "BREAKING-CHANGE:"):
			level = 3
		case c != nil && c[1] == "feat":
			level = max(level, 2)
		default:
			level = max(level, 1)
		}
	}
	switch level {
	case 3:
		return fmt.Sprintf("%d.0.0", major+1), nil
	case 2:
		return fmt.Sprintf("%d.%d.0", major, minor+1), nil
	case 1:
		return fmt.Sprintf("%d.%d.%d", major, minor, patch+1), nil
	}
	return latest, nil
}

// NextVersion reads the newest stable vX.Y.Z tag reachable from HEAD in dir and the
// commits after it. A tagged HEAD keeps its own version.
func NextVersion(dir string) (string, error) {
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(errb.String()))
		}
		return out.String(), nil
	}
	tags, err := git("tag", "--merged", "HEAD", "--list", "v*", "--sort=-v:refname")
	if err != nil {
		return "", err
	}
	latest := ""
	for _, t := range strings.Fields(tags) {
		if ValidTag(t) {
			latest = t
			break
		}
	}
	if latest == "" {
		return "", errors.New("no stable vX.Y.Z tag is reachable; fetch tags with full history")
	}
	log, err := git("log", "--format=%B%x00", latest+"..HEAD")
	if err != nil {
		return "", err
	}
	var messages []string
	for _, m := range strings.Split(log, "\x00") {
		if m = strings.TrimSpace(m); m != "" {
			messages = append(messages, m)
		}
	}
	return Bump(strings.TrimPrefix(latest, "v"), messages)
}
