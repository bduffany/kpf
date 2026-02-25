GO_SRCS := $(shell find . -name "*.go")
BINARY := kpf

all: $(BINARY)

build: $(BINARY)
.PHONY: build

test: go.sum
	go test ./...

.PHONY: test

lint: go.sum
	@unformatted=$$(gofmt -l $(GO_SRCS)); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt required for:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	go vet ./...

.PHONY: lint

tidy-check: SKIP_TIDY=1
tidy-check: go.sum
	@git diff --exit-code -- go.mod go.sum >/dev/null || (echo "go.mod/go.sum not tidy" && exit 1)

.PHONY: tidy-check

go.sum: go.mod $(GO_SRCS)
	@if [ "$(SKIP_TIDY)" != "1" ]; then go mod tidy; fi

$(BINARY): go.sum $(GO_SRCS)
	go build -o $(BINARY) .
