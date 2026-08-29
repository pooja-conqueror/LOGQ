package formats

import (
	"bufio"
	"compress/gzip"
	"io"
)

// gzipMagic is the two-byte gzip member header (RFC 1952).
var gzipMagic = [2]byte{0x1f, 0x8b}

// MaybeGunzip sniffs r's first two bytes for the gzip magic number and, if
// present, wraps r in a transparent gzip reader — multi-member streams are
// supported for free, since compress/gzip.Reader defaults to
// Multistream(true), no extra wiring needed. If the magic bytes aren't
// present, the returned reader still yields r's content byte-for-byte —
// whatever the sniff peeked at is buffered, not consumed and lost.
func MaybeGunzip(r io.Reader) (io.Reader, error) {
	br := bufio.NewReader(r)
	magic, err := br.Peek(2)
	if err != nil {
		// Fewer than 2 bytes total (empty or 1-byte input): definitely not
		// gzip. Not a real failure of the sniff itself — hand back
		// whatever little the stream has.
		return br, nil
	}
	if magic[0] == gzipMagic[0] && magic[1] == gzipMagic[1] {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		return gz, nil
	}
	return br, nil
}
