package main

import (
	"strings"
	"testing"
)

func TestMigrateConfigLines(t *testing.T) {
	src := `python_bin = "x"
[router]
port = 8080

# Chat slot.
[[backend]]
name   = "valkyrie"
engine = "lm"
mode   = "swap"
group  = "chat"
host   = "127.0.0.1"
port   = 1234
model  = "/m/valkyrie"

[[backend]]
name   = "embed"
engine = "embed"
mode   = "persistent"
host   = "127.0.0.1"
port   = 8081
model  = "/m/embed"

[[backend]]
name = "remote-gpt"
mode = "external"
url  = "http://other:8080"
`
	out, changed := migrateConfigLines(src)
	if !changed {
		t.Fatal("expected changes")
	}
	for _, l := range strings.Split(out, "\n") {
		k, _, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "mode", "group":
			t.Errorf("legacy key survived: %q", l)
		}
	}
	for _, want := range []string{`slot`, "warm   = true", "remote = true", "# Chat slot.", `model  = "/m/valkyrie"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	// idempotent
	if out2, changed2 := migrateConfigLines(out); changed2 || out2 != out {
		t.Errorf("migration not idempotent")
	}
}
