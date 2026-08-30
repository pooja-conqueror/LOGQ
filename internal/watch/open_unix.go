//go:build !windows

package watch

import "os"

// openForTailing opens path for reading, safe to hold across polls even
// if the file is later removed or replaced by an external rotator — on
// Unix this is just os.Open: unlinking (or renaming away) a file with an
// open reader is already always safe, the reader keeps seeing the OLD
// file's content via its existing descriptor. No special handling
// needed here at all; see open_windows.go for why Windows does.
func openForTailing(path string) (*os.File, error) {
	return os.Open(path)
}
