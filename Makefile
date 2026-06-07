.PHONY: build install test vet fmt clean

BIN := bin/cambly

build:
	go build -o $(BIN) ./cmd/cambly

install:
	go install ./cmd/cambly

vet:
	go vet ./...

fmt:
	gofmt -l -w .

test:
	go test ./...

clean:
	rm -rf bin
