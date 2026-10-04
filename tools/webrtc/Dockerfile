FROM golang:1.26-alpine
RUN apk add --no-cache ffmpeg coturn
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go run ./cmd/qgramm-build build -config tools/webrtc/calls.toml -out /usr/local/bin/qgramm-calls
WORKDIR /src/tools/webrtc
RUN go mod download && go build -mod=readonly -o /usr/local/bin/qgramm-webrtc-check .
RUN ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc=size=160x120:rate=10 -t 1 -pix_fmt yuv420p -c:v libvpx -f ivf /tmp/video.ivf && ffmpeg -hide_banner -loglevel error -f lavfi -i sine=frequency=440:sample_rate=48000 -t 1 -c:a libopus -page_duration 20000 /tmp/audio.ogg
CMD ["/usr/local/bin/qgramm-webrtc-check"]
