.PHONY: build test test-race fmt vet check clean

GO ?= go
BINARY ?= cloudpan189-go
OUTPUT_DIR ?= out/bin

build:
	@mkdir -p $(OUTPUT_DIR)
	$(GO) build -o $(OUTPUT_DIR)/$(BINARY) .

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')

vet:
	$(GO) vet ./...

check: vet test build

clean:
	rm -rf $(OUTPUT_DIR)
