// Package watch implements portable poll-tail file watching for -w
// (§14/EC-42/EC-43) — no fsnotify (cgo on some platforms; a Windows
// build without a C toolchain, verified directly in this project's own
// dev environment, can't even build with a cgo dependency at all), just
// a plain os.Stat poll loop. Rotation detection (a file replaced, or
// truncated in place) uses stdlib os.SameFile, which is itself already
// portable across platforms (dev+inode on Unix, the file index via
// BY_HANDLE_FILE_INFORMATION on Windows) — no platform-specific code
// needed in this package at all.
package watch

import (
	"context"
	"io"
	"os"
	"time"
)

// DefaultPollInterval is -w's poll interval unless --watch=SECONDS
// overrides it.
const DefaultPollInterval = time.Second

// Tailer polls one file path for newly-appended content, portably
// detecting rotation via os.Stat + os.SameFile.
type Tailer struct {
	path string

	file         *os.File
	info         os.FileInfo // last known identity+size; nil whenever the file is currently unopened (missing, or not yet bootstrapped)
	offset       int64
	bootstrapped bool // true once the very first successful stat has been handled
}

// NewTailer creates a Tailer for path. Nothing is opened yet — the first
// call to Poll does that.
func NewTailer(path string) *Tailer {
	return &Tailer{path: path}
}

// Poll checks the file once. data is any newly-appended bytes since the
// last successful Poll — empty (not nil-vs-non-nil meaningful, just
// possibly zero-length) when nothing changed. The very first Poll of a
// Tailer's life never returns data: it skips straight to the file's
// current end, matching `tail -f`'s own convention — watch mode surfaces
// NEW activity from the moment it starts, not a replay of everything
// already in the file.
//
// rotated is true whenever this poll discarded the previous read
// position and started over from byte 0 of a (possibly brand new) file:
//   - the file was missing on a previous poll and has just reappeared
//     (EC-42: deleted then recreated — its new content IS the new
//     activity, unlike the very-first-ever poll's EOF-skip above);
//   - the file at this path now has a different identity than before
//     (os.SameFile false) even though it was never observed missing in
//     between — an atomic rename-based rotation can look like this;
//   - the file's size has shrunk below where Tailer had already read to
//     (EC-43: copytruncate — truncated in place, same identity).
//
// A stat/open failure that looks like "the file doesn't exist right
// now" is not an error at all — Poll just reports no data this round and
// tries again next time, since a rotation is very often exactly a
// delete-then-recreate race spanning more than one poll interval. Any
// other error (permission denied, a genuine read failure once opened) is
// returned as err.
func (t *Tailer) Poll() (data []byte, rotated bool, err error) {
	fi, statErr := os.Stat(t.path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			t.closeAndForget()
			return nil, false, nil
		}
		return nil, false, statErr
	}

	switch {
	case t.info == nil && !t.bootstrapped:
		if openErr := t.openFresh(fi, fi.Size()); openErr != nil {
			return nil, false, nil // raced with deletion between Stat and Open; retry next poll
		}
		t.bootstrapped = true
		return nil, false, nil

	case t.info == nil:
		if openErr := t.openFresh(fi, 0); openErr != nil {
			return nil, false, nil
		}
		rotated = true

	case !os.SameFile(t.info, fi):
		if openErr := t.openFresh(fi, 0); openErr != nil {
			return nil, false, nil
		}
		rotated = true

	case fi.Size() < t.offset:
		if openErr := t.openFresh(fi, 0); openErr != nil {
			return nil, false, nil
		}
		rotated = true
	}

	if fi.Size() > t.offset {
		buf := make([]byte, fi.Size()-t.offset)
		n, readErr := t.file.ReadAt(buf, t.offset)
		if readErr != nil && readErr != io.EOF {
			return nil, rotated, readErr
		}
		t.offset += int64(n)
		data = buf[:n]
	}

	t.info = fi
	return data, rotated, nil
}

func (t *Tailer) openFresh(fi os.FileInfo, offset int64) error {
	if t.file != nil {
		_ = t.file.Close()
	}
	f, err := openForTailing(t.path)
	if err != nil {
		t.info = nil
		t.file = nil
		return err
	}
	t.file = f
	t.offset = offset
	t.info = fi
	return nil
}

func (t *Tailer) closeAndForget() {
	if t.file != nil {
		_ = t.file.Close()
		t.file = nil
	}
	t.info = nil
}

// Close releases the underlying open file handle, if any.
func (t *Tailer) Close() error {
	if t.file == nil {
		return nil
	}
	err := t.file.Close()
	t.file = nil
	return err
}

// Loop polls t every interval until ctx is done, calling onData for each
// poll that produced new data or a rotation event (a poll with neither
// is silently skipped — nothing changed, nothing to report). Returns
// ctx.Err() once ctx is done, or whatever error onData/Poll returned.
func Loop(ctx context.Context, t *Tailer, interval time.Duration, onData func(data []byte, rotated bool) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			data, rotated, err := t.Poll()
			if err != nil {
				return err
			}
			if len(data) == 0 && !rotated {
				continue
			}
			if err := onData(data, rotated); err != nil {
				return err
			}
		}
	}
}
