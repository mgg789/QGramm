FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -tags=qg_bench_profile -o /out/qgramm ./cmd/qgramm
FROM alpine:3.22
RUN addgroup -g 10001 qgramm && adduser -D -u 10001 -G qgramm qgramm && mkdir /data && chown qgramm:qgramm /data
COPY --from=build /out/qgramm /app/qgramm
USER 10001:10001
ENTRYPOINT ["/app/qgramm"]
