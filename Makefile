.PHONY: build test test-unit-cover lint lint-arch clean fmt vet tidy

build:
	go build ./...

test:
	go test ./... -v -count=1 -race

test-unit-cover:
	go test -race -coverprofile=coverage.out ./...

lint:
	golangci-lint run ./...

lint-arch:
	go run github.com/fe3dback/go-arch-lint@v1.15.0 check --project-path .

fmt:
	gofmt -w .

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -f coverage.out
