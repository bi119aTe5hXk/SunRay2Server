.PHONY: build test vet run clean

GOCACHE ?= $(CURDIR)/.cache/go-build

build:
	GOCACHE="$(GOCACHE)" GOENV=off go build -o sunrayd ./cmd/sunrayd

test:
	GOCACHE="$(GOCACHE)" GOENV=off go test ./...

vet:
	GOCACHE="$(GOCACHE)" GOENV=off go vet ./...

run:
	GOCACHE="$(GOCACHE)" GOENV=off go run ./cmd/sunrayd

clean:
	go clean

