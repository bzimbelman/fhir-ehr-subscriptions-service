# Dockerfile for cmd/test-ws-subscriber.
#
# Same shape as Dockerfile.resthook: two-stage build, distroless-ish
# runtime image. The healthcheck uses wget so docker-compose's
# CMD-form healthcheck can run without a shell.

FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/test-ws-subscriber ./cmd/test-ws-subscriber
RUN printf '#!/bin/sh\nwget -qO- http://127.0.0.1:8091/healthz >/dev/null 2>&1\n' > /out/test-ws-subscriber-healthcheck \
    && chmod +x /out/test-ws-subscriber-healthcheck

FROM alpine:3.19
RUN apk add --no-cache wget ca-certificates
COPY --from=build /out/test-ws-subscriber /usr/local/bin/test-ws-subscriber
COPY --from=build /out/test-ws-subscriber-healthcheck /usr/local/bin/test-ws-subscriber-healthcheck
EXPOSE 8091
ENTRYPOINT ["/usr/local/bin/test-ws-subscriber"]
CMD ["-addr", ":8091"]
