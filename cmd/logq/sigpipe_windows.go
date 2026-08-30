//go:build windows

package main

import (
	"errors"
	"syscall"
)

// isBrokenPipe reports whether err is the write-side signature of a
// downstream reader closing early (e.g. `logq ... | head -1`). Windows
// has no SIGPIPE at all — empirically confirmed on this dev machine
// (writing in a loop to stdout piped through `head`, via Git Bash/MSYS)
// a write past the point the reader closes its end fails with
// ERROR_NO_DATA (Errno 232, "The pipe is being closed."), not the
// ERROR_BROKEN_PIPE (109) name might suggest — that one is more
// typically seen on the READ side. Checking both is cheap and avoids
// silently missing whichever pipe implementation (anonymous vs named,
// different Windows versions) happens to surface the other one instead.
func isBrokenPipe(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	const (
		errorBrokenPipe = 109
		errorNoData     = 232
	)
	return errno == errorBrokenPipe || errno == errorNoData
}
