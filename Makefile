.PHONY: build build-metrics build-mcp build-all run run-metrics clean

build:
	go build -o argus .

build-metrics:
	go build -o argus-metrics-server ./cmd/metrics-server

build-mcp:
	go build -o argus-mcp-server ./cmd/mcp-server

build-all: build build-metrics build-mcp

run:
	go run .

run-metrics:
	go run ./cmd/metrics-server

clean:
	rm -f argus argus-metrics-server argus-mcp-server
