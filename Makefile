# All Go commands run inside Docker so the host stays clean.
# Tag kept for readability; the digest is what is pulled.
GO_IMAGE   := golang:1.27.4-trixie@sha256:f18c3d73a184fb0582828510722e723b63a93f7188d11dd223d9be6f6880e65d
DOCKER_RUN := docker run --rm \
	-v $(CURDIR):/src -w /src \
	-v pkms-gomod:/go/pkg/mod \
	-v pkms-gobuild:/root/.cache/go-build \
	$(GO_IMAGE)

.PHONY: test test-race e2e pdf-eval vet lint build build-host tidy fmt

test:
	$(DOCKER_RUN) go test ./...

# What CI's test step runs (the race detector needs cgo, so no CGO_ENABLED=0).
# 30m: race instrumentation multiplies the per-child wasm compile that every
# PDF extraction test pays (§31.13), overrunning go test's 10m default.
test-race:
	$(DOCKER_RUN) go test -race -timeout 45m ./...

# New-user-experience walkthrough as executable scripts (e2e/testdata/).
e2e:
	$(DOCKER_RUN) go test -tags=e2e ./e2e/...

# §31.12 PDF readability scorecard — a measurement tool, not a CI gate.
# This is the pinned measurement environment: numbers measured elsewhere
# do not satisfy or violate the §31.12 budgets.
pdf-eval:
	$(DOCKER_RUN) go test -tags=pdfeval -run 'TestPDFEval$$' -v ./internal/ingest/

vet:
	$(DOCKER_RUN) go vet ./...

# vet + formatting + golangci-lint (same version/flags as CI), so `make
# lint` locally catches everything the pull-request gate does.
GOLANGCI_IMAGE := golangci/golangci-lint:v2.15.0@sha256:fbfcfd12a6f8d7f1b97b3362ca80fc2a244d46863148c73cb88e7793af96c481

lint: vet
	$(DOCKER_RUN) sh -c 'test -z "$$(gofmt -l ./cmd ./internal)" || (gofmt -l ./cmd ./internal && exit 1)'
	docker run --rm -v $(CURDIR):/src -w /src \
		-v pkms-gomod:/go/pkg/mod -v pkms-gobuild:/root/.cache/go-build \
		$(GOLANGCI_IMAGE) golangci-lint run --build-tags e2e,pdfeval

# Linux binary (native to the container).
build:
	$(DOCKER_RUN) env CGO_ENABLED=0 go build -trimpath -o dist/pkms ./cmd/pkms

# Cross-compiled binary for the host machine (macOS arm64).
build-host:
	$(DOCKER_RUN) env CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
		go build -trimpath -o dist/pkms-darwin-arm64 ./cmd/pkms

tidy:
	$(DOCKER_RUN) go mod tidy

fmt:
	$(DOCKER_RUN) gofmt -w ./cmd ./internal
