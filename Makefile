BINARY   := vaultnuban
CMD      := ./cmd/server
IMAGE    := vaultnuban:local

.PHONY: build run test vet lint docker-build docker-run clean

## build: compile the server binary
build:
	go build -o $(BINARY) $(CMD)

## run: build and start the server (reads .env if present)
run: build
	@if [ -f .env ]; then set -a && . ./.env && set +a; fi && ./$(BINARY)

## test: run all tests including the harness scenarios
test:
	go test ./... -count=1 -race

## vet: run go vet
vet:
	go vet ./...

## harness: run only the reconciliation harness
harness:
	go test ./harness/... -v -count=1

## docker-build: build the production Docker image
docker-build:
	docker build -t $(IMAGE) .

## docker-run: run the image locally (requires .env file)
docker-run: docker-build
	docker run --rm --env-file .env -p 8080:8080 $(IMAGE)

## clean: remove built binary
clean:
	rm -f $(BINARY)
