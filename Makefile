.PHONY: build test fuzz proof

build:
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -o bin/logq ./cmd/logq

test:
	go test ./...

# 60s smoke fuzz run (matches the CI budget from Phase 11) across every
# native testing.F target in the tree — currently just the logfmt
# round-trip property, more join as later phases add their own.
fuzz:
	go test -fuzz=FuzzLogfmtRoundTrip -fuzztime=60s ./internal/logfmtx/

# Regenerates deps-proof.txt. Filters out both Go's standard library
# (.Standard) AND this module's own packages (.Module.Main) — without the
# second filter, go list would also report logq's own internal/* packages
# as "non-standard," which is true but not what the zero-dependency claim
# is about. A passing proof is this file being empty.
proof:
	go list -deps -f '{{if and (not .Standard) (not .Module.Main)}}{{.ImportPath}}{{end}}' ./... > deps-proof.txt
