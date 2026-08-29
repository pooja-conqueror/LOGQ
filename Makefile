.PHONY: build

build:
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -o bin/logq ./cmd/logq
