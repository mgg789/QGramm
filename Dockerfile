FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG CONFIG=configs/container.toml
# qgramm-build validates CONFIG, selects feature tags, and embeds their manifest.
RUN CGO_ENABLED=0 go run ./cmd/qgramm-build build -config "$CONFIG" -out /out/qgramm
RUN cp "$CONFIG" /out/qgramm.toml

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -g 10001 qgramm && adduser -D -u 10001 -G qgramm qgramm && mkdir /data && chown qgramm:qgramm /data
WORKDIR /app
COPY --from=build /out/qgramm /app/qgramm
COPY --from=build /out/qgramm.toml /app/qgramm.toml
COPY LICENSE NOTICE /app/
USER 10001:10001
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD /app/qgramm -healthcheck http://127.0.0.1:8080/readyz
ENTRYPOINT ["/app/qgramm"]
CMD ["-config", "/app/qgramm.toml"]
