// Copyright 2026 by moshix

package shell

import (
	"os"
	"strings"
	"testing"
)

// TestReadmeHasEveryCommand fails when a command of the help table is
// not documented in README.md (as a `command ...` code span).
func TestReadmeHasEveryCommand(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	for _, e := range helpTable {
		if !strings.Contains(readme, "`"+e.cmd+" ") && !strings.Contains(readme, "`"+e.cmd+"`") {
			t.Errorf("README.md does not document the %q command", e.cmd)
		}
	}
}
