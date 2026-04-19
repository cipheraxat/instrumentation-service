.PHONY: all generate build test run clean docker lint

all: generate build

# --- protobuf code generation ---
generate:
	@echo "==> Generating protobuf Go code…"
	@mkdir -p gen/telemetryv1
	protoc --proto_path=proto \
	       --go_out=.     --go_opt=module=github.com/cipheraxat/instrumentation-service \
	       --go-grpc_out=. --go-grpc_opt=module=github.com/cipheraxat/instrumentation-service \
	       proto/telemetry/v1/telemetry.proto
	@echo "==> go mod tidy"
	go mod tidy

build:
	go build -o bin/server ./cmd/server

test:
	go test -v -race -count=1 ./...

run: build
	./bin/server

clean:
	rm -rf bin/ gen/

docker:
	docker compose up --build

lint:
	golangci-lint run ./...
