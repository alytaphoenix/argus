.PHONY: build build-metrics build-all run run-metrics clean

build:
	go build -o ratataskr .

build-metrics:
	go build -o ratataskr-metrics-server ./cmd/metrics-server

build-all: build build-metrics

run:
	go run .

run-metrics:
	go run ./cmd/metrics-server

clean:
	rm -f ratataskr ratataskr-metrics-server
