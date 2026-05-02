.PHONY: build run test vet tidy docker clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN_DIR := bin

build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/mht .

run: build
	./$(BIN_DIR)/mht

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

# Build context must be the parent (Situation-Awareness/) so the SAW proto can be vendored.
docker:
	cd .. && docker build -t saw-mht:$(VERSION) -f MHT/Dockerfile .

clean:
	rm -rf $(BIN_DIR)
