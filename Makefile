IMAGE ?= ghcr.io/sympozium-ai/ergoz
TAG   ?= dev

.PHONY: build test vet fmt docker-build clean

build:
	go build -o bin/ ./cmd/...

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l . && test -z "$$(gofmt -l .)"

docker-build:
	docker buildx build --load -t $(IMAGE):$(TAG) -f images/Dockerfile .

clean:
	rm -rf bin/
