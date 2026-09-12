BINARY  := pve-optimizer
VERSION := $(shell cat VERSION)
LDFLAGS := -s -w \
	-X $(BINARY)/internal/version.Version=$(VERSION) \
	-X $(BINARY)/internal/version.BuiltAt=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.DEFAULT_GOAL := help

help: ## Diese Übersicht anzeigen
	@grep -hE '^[a-z][a-zA-Z0-9_-]*:.*?##' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: check ## Binary für den eigenen Rechner bauen
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

build-linux: check ## Linux-Binaries (amd64 + arm64) bauen — das, was auf Proxmox läuft
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 ./cmd/$(BINARY)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 ./cmd/$(BINARY)

check: fmt vet lint test vulncheck ## Alles, was auch die CI prüft

fmt: ## Formatierung prüfen
	@test -z "$$(gofmt -l .)" || { echo "nicht formatiert:"; gofmt -l .; exit 1; }

vet: ## go vet
	go vet ./...

lint: ## golangci-lint
	golangci-lint run ./...

test: ## Tests
	go test ./...

vulncheck: ## Bekannte Schwachstellen in den Abhängigkeiten
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

clean: ## Build-Ergebnisse entfernen
	rm -rf bin/

.PHONY: help build build-linux check fmt vet lint test vulncheck clean
