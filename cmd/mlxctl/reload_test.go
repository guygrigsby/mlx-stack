package main

import (
	"fmt"
	"io/fs"
	"syscall"
	"testing"
)

func TestIsDaemonDown(t *testing.T) {
	down := []error{
		syscall.ECONNREFUSED,
		syscall.ENOENT,
		fs.ErrNotExist,
		fmt.Errorf("dial unix /x/admin.sock: connect: %w", syscall.ECONNREFUSED),
		fmt.Errorf("open socket: %w", fs.ErrNotExist),
	}
	for _, err := range down {
		if !isDaemonDown(err) {
			t.Errorf("want daemon-down for %v", err)
		}
	}

	notDown := []error{
		fmt.Errorf("admin POST /v1/reload: 500: boom"),
		fmt.Errorf("some unrelated error"),
		nil,
	}
	for _, err := range notDown {
		if isDaemonDown(err) {
			t.Errorf("should not be daemon-down for %v", err)
		}
	}
}
