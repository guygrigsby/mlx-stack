package main

import (
	"bytes"
	"strings"
	"testing"
)

// Errors must reach the user exactly once: main() prints the error returned by
// Execute(), so cobra itself must stay silent (SilenceErrors). Before the fix
// every error printed twice.
func TestRootCmd_ErrorsPrintOnce(t *testing.T) {
	root := newRootCmd()
	var errBuf bytes.Buffer
	root.SetErr(&errBuf)
	root.SetOut(&errBuf)
	root.SetArgs([]string{"start"}) // missing required arg -> error

	err := root.Execute()
	if err == nil {
		t.Fatal("start with no args should error")
	}
	// cobra must not have printed the error itself; main() is the one printer.
	if s := errBuf.String(); strings.Contains(s, err.Error()) {
		t.Errorf("cobra printed the error (would appear twice with main's print):\n%s", s)
	}
}

// The global --output validator must still run and reject bad values.
func TestRootCmd_RejectsBadOutputFormat(t *testing.T) {
	root := newRootCmd()
	root.SetErr(&bytes.Buffer{})
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"health", "-o", "yaml"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "invalid --output") {
		t.Fatalf("want invalid --output error, got %v", err)
	}
}
