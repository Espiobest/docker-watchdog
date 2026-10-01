.PHONY: build test check demo clean-demo

build:
	go build -trimpath -o bin/watchdog ./cmd/watchdog

test:
	go test -race -cover ./...

check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...

demo: build
	docker compose -p watchdog-demo up -d
	./bin/watchdog --label=watchdog.demo=true --auto-restart --recover-exited

clean-demo:
	docker compose -p watchdog-demo --profile service down
