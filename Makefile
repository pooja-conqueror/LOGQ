.PHONY: build test proof

build:
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -o bin/logq ./cmd/logq

test:
	go test ./...

# Regenerates deps-proof.txt. Filters out both Go's standard library
# (.Standard) AND this module's own packages (.Module.Main) — without the
# second filter, go list would also report logq's own internal/* packages
# as "non-standard," which is true but not what the zero-dependency claim
# is about. A passing proof is this file being empty.
proof:
	go list -deps -f '{{if and (not .Standard) (not .Module.Main)}}{{.ImportPath}}{{end}}' ./... > deps-proof.txt
