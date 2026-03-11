FROM golang:1.24-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations

RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o /out/qgramm-server ./cmd/server

FROM alpine:3.21
WORKDIR /app

RUN addgroup -S qgramm && adduser -S qgramm -G qgramm

COPY --from=builder /out/qgramm-server /app/qgramm-server
COPY migrations /app/migrations
COPY configs /app/configs

RUN mkdir -p /app/data && chown -R qgramm:qgramm /app

USER qgramm
EXPOSE 8080

CMD ["/app/qgramm-server"]
