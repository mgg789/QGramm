FROM golang:1.26-alpine
RUN apk add --no-cache nodejs npm chromium
WORKDIR /src/tools/browser-webrtc
COPY tools/browser-webrtc/package.json tools/browser-webrtc/package-lock.json ./
RUN npm ci --ignore-scripts
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY tools/browser-webrtc/calls-build.toml ./tools/browser-webrtc/calls-build.toml
RUN go run ./cmd/qgramm-build build -config tools/browser-webrtc/calls-build.toml -out /usr/local/bin/qgramm-browser-calls
COPY tools/browser-webrtc/run.mjs tools/browser-webrtc/page.js tools/browser-webrtc/pcm.js ./tools/browser-webrtc/
WORKDIR /src/tools/browser-webrtc
ENV QGRAMM_BROWSER_SERVER_BINARY=/usr/local/bin/qgramm-browser-calls
ENV QGRAMM_BROWSER_EXECUTABLE=/usr/bin/chromium-browser
CMD ["node", "run.mjs"]
