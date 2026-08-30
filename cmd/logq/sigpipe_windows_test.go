//go:build windows

package main

import (
	"syscall"
	"testing"
)

func TestIsBrokenPipe_WindowsKnownErrnos(t *testing.T) {
	// ERROR_NO_DATA (232) is the one empirically confirmed on this dev
	// machine (see sigpipe_windows.go's doc comment); ERROR_BROKEN_PIPE
	// (109) is checked defensively for other pipe implementations.
	for _, errno := range []syscall.Errno{109, 232} {
		if !isBrokenPipe(errno) {
			t.Errorf("isBrokenPipe(syscall.Errno(%d)) = false, want true", errno)
		}
	}
	if isBrokenPipe(syscall.Errno(5)) { // ERROR_ACCESS_DENIED, an unrelated real errno
		t.Error("isBrokenPipe(syscall.Errno(5)) = true, want false")
	}
}
