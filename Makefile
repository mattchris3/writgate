.PHONY: build test lint

build:
	go build -o writgate .

test:
	go test -race ./...

lint:
	golangci-lint run
