//go:build windows

package watch

import (
	"os"
	"syscall"
)

// openForTailing opens path for reading with FILE_SHARE_DELETE
// explicitly included. Go's plain os.Open on Windows does NOT set this
// share flag — empirically confirmed on this dev machine: os.Remove and
// os.Rename against a file held open via a plain os.Open both fail with
// "The process cannot access the file because it is being used by
// another process." Left unfixed, that would be a real Windows-only
// limitation: an external log rotator could never rotate a file `logq
// -w` is watching, for as long as logq keeps it open — silently
// breaking the exact EC-42/EC-43 rotation scenarios this package exists
// to handle correctly. FILE_SHARE_DELETE tells Windows to allow exactly
// that, matching Unix's own "unlink/rename while open is always fine"
// behavior, which is the actual invariant Tailer's rotation detection
// (os.Stat + os.SameFile in tailer.go) is designed around.
func openForTailing(path string) (*os.File, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := syscall.CreateFile(
		pathPtr,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}
