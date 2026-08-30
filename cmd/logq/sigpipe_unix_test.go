//go:build !windows

package main

import (
	"syscall"
	"testing"
)

func TestIsBrokenPipe_UnixEPIPE(t *testing.T) {
	if !isBrokenPipe(syscall.EPIPE) {
		t.Error("isBrokenPipe(syscall.EPIPE) = false, want true")
	}
	if isBrokenPipe(syscall.EACCES) { // an unrelated real errno
		t.Error("isBrokenPipe(syscall.EACCES) = true, want false")
	}
}
