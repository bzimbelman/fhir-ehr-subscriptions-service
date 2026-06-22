# Dockerfile for cmd/test-receivers — the consolidated channel-receiver
# fixture for the e2e/realstack stack.
#
# OP #345 (Realstack simplification B) merges the four legacy receiver
# fixtures (test-resthook-subscriber, test-ws-subscriber,
# test-mllp-control-plane, mailpit) into one Go binary that exposes
# every receiver port concurrently:
#
#	:8090   rest-hook subscriber
#	:8091   websocket subscriber query API
#	:8093   MLLP control plane HTTP API
#	:1025   SMTP receiver (replaces mailpit)
#	:1080   SMTP query API
#
# Build context is the repo root. The two-stage build keeps the runtime
# image small (gcr.io/distroless/static-debian12:nonroot) and keeps
# the Go toolchain off the captured-attack surface. Distroless ships
# pre-baked CA certs and runs as nonroot by default; we COPY a small
# Go-built healthcheck shim so the docker-compose CMD-form healthcheck
# does not need a shell or wget.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/test-receivers ./cmd/test-receivers
# Build a tiny healthcheck binary inline. Probes every required
# subsystem's /healthz; any non-200 returns non-zero and the
# docker-compose healthcheck reports the container unhealthy.
# The MLLP listener only opens when FHIRSUBS_MLLP_TARGET is set, so
# the probe skips it when the env var is empty.
RUN mkdir /healthcheck && cat > /healthcheck/main.go <<'EOF'
package main

import (
	"net/http"
	"os"
	"time"
)

func probe(url string) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func main() {
	urls := []string{
		"http://127.0.0.1:8090/healthz",
		"http://127.0.0.1:8091/healthz",
		"http://127.0.0.1:1080/healthz",
	}
	for _, u := range urls {
		if !probe(u) {
			os.Exit(1)
		}
	}
	if os.Getenv("FHIRSUBS_MLLP_TARGET") != "" {
		if !probe("http://127.0.0.1:8093/healthz") {
			os.Exit(1)
		}
	}
}
EOF
RUN cd /healthcheck && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/test-receivers-healthcheck .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/test-receivers /usr/local/bin/test-receivers
COPY --from=build /out/test-receivers-healthcheck /usr/local/bin/test-receivers-healthcheck
EXPOSE 8090 8091 8093 1025 1080
ENTRYPOINT ["/usr/local/bin/test-receivers"]
