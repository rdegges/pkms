# All Go commands run inside Docker so the host stays clean.
GO_IMAGE   := golang:1.26.5-trixie
DOCKER_RUN := docker run --rm \
	-v $(CURDIR):/src -w /src \
	-v pkms-gomod:/go/pkg/mod \
	-v pkms-gobuild:/root/.cache/go-build \
	$(GO_IMAGE)

.PHONY: test vet build build-host tidy fmt

test:
	$(DOCKER_RUN) go test ./...

vet:
	$(DOCKER_RUN) go vet ./...

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
