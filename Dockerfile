# --- generate -----------------------------------------------------------
FROM golang:1.22-alpine AS proto

RUN apk add --no-cache protobuf-dev make
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.33.0 && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.3.0

WORKDIR /app
COPY proto/ proto/
RUN mkdir -p gen/telemetryv1 && \
    protoc --proto_path=proto \
           --go_out=.     --go_opt=module=github.com/cipheraxat/instrumentation-service \
           --go-grpc_out=. --go-grpc_opt=module=github.com/cipheraxat/instrumentation-service \
           proto/telemetry/v1/telemetry.proto

# --- build --------------------------------------------------------------
FROM golang:1.22-alpine AS build

WORKDIR /app
COPY go.mod ./
RUN go mod download || true
COPY . .
COPY --from=proto /app/gen/ gen/
RUN go mod tidy && CGO_ENABLED=0 go build -o /server ./cmd/server

# --- runtime -------------------------------------------------------------
FROM alpine:3.19

RUN apk add --no-cache ca-certificates
COPY --from=build /server /server

EXPOSE 8080 9090 2112
CMD ["/server"]
