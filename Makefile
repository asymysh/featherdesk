BINARY := viewport-rds
PKG := ./...
CMD := ./cmd/server

.PHONY: build test lint fmt clean vet

build:
	go build -o $(BINARY) $(CMD)

test:
	go test -race -count=1 $(PKG)

cover:
	go test -race -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out

lint:
	golangci-lint run $(PKG)

fmt:
	go fmt $(PKG)

vet:
	go vet $(PKG)

clean:
	rm -f $(BINARY) coverage.out

check: fmt vet lint test
