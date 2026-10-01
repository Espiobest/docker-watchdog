FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /watchdog ./cmd/watchdog

FROM alpine:3.23
RUN apk add --no-cache ca-certificates
COPY --from=build /watchdog /usr/local/bin/watchdog
ENTRYPOINT ["watchdog"]
