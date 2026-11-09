// Copyright 2026 by moshix

package shell

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultHistoryPath returns ~/.gonje_history ("" if there is no home).
func DefaultHistoryPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gonje_history")
}

// LoadHistory reads up to HistoryMax lines. A missing or unreadable file
// yields an empty history.
func LoadHistory(path string) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimRight(sc.Text(), "\r"); strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	if len(out) > HistoryMax {
		out = out[len(out)-HistoryMax:]
	}
	return out
}

// AppendHistory appends one line; errors (read-only home, ...) are
// ignored on purpose: history is a convenience. When the file grows well
// past HistoryMax it is rewritten with the newest HistoryMax lines.
func AppendHistory(path, line string) {
	if path == "" || strings.ContainsAny(line, "\n\r") {
		return
	}
	line = maskSecrets(line)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line + "\n")
	st, err := f.Stat()
	_ = f.Close()
	if err == nil && st.Size() > 256*1024 {
		lines := LoadHistory(path)
		_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	}
}

// secretRE matches password=<value> and remote_password=<value> (any
// case, '-' for '_') as a word of a command line.
var secretRE = regexp.MustCompile(`(^|[\s,])((?i:remote[_-]password|password)=)[^\s,]*`)

// maskSecrets replaces password values in a command line with "***",
// for the log, the history and the echoed command.
func maskSecrets(line string) string {
	return secretRE.ReplaceAllString(line, "${1}${2}***")
}
