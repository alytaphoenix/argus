.PHONY: build build-metrics build-mcp build-all dist-linux install run run-metrics \
        fmt test test-race vet lint vuln check release-snapshot clean

VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o argus .

build-metrics:
	go build -ldflags "$(LDFLAGS)" -o argus-metrics-server ./cmd/metrics-server

build-mcp:
	go build -ldflags "$(LDFLAGS)" -o argus-mcp-server ./cmd/mcp-server

build-all: build build-metrics build-mcp

install:
	go install -ldflags "$(LDFLAGS)" .

# Cross-compile the binaries installed by scripts/bootstrap-pve.sh.
dist-linux:
	mkdir -p dist/linux-amd64
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/linux-amd64/argus .
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/linux-amd64/argus-metrics-server ./cmd/metrics-server
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/linux-amd64/argus-mcp-server ./cmd/mcp-server

fmt:
	gofmt -w .

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

lint:
	staticcheck ./...

vuln:
	govulncheck ./...

check: vet test lint vuln

release-snapshot:
	goreleaser release --snapshot --clean

run:
	go run .

run-metrics:
	go run ./cmd/metrics-server

clean:
	rm -f argus argus-metrics-server argus-mcp-server
	rm -rf dist
