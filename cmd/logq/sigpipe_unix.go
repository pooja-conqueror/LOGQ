//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// isBrokenPipe reports whether err is the write-side signature of a
// downstream reader closing early (e.g. `logq ... | head -1`) — on Unix,
// a write to a closed pipe fails with EPIPE (Go's runtime already
// converts the underlying SIGPIPE into this ordinary error for
// stdout/stderr, rather than terminating the process outright).
func isBrokenPipe(err error) bool {
	return errors.Is(err, syscall.EPIPE)
}
