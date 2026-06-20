# syntax=docker/dockerfile:1.7

# ---- Build stage ----------------------------------------------------------
# BUILDPLATFORM is the platform of the build host; TARGETOS / TARGETARCH are
# the platform we are building for. Combined with `docker buildx build
# --platform linux/amd64,linux/arm64`, this produces a multi-arch image with
# one native-host build per arch (cross-compiled by the Go toolchain).
# OP #134: pin Go toolchain exactly. The wildcard '1.22-alpine' tag
# silently rolls forward; an exact patch keeps image builds reproducible
# and aligned with the .github/workflows/ pin and go.mod.
FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS build

ARG TARGETOS
ARG TARGETARCH

# OP #210: VERSION and COMMIT are embedded into the binary via -ldflags
# so the running container reports the build it was cut from. CI passes
# these via --build-arg from the matching tag/SHA; an unset value
# leaves the in-source default ("dev") in place.
ARG VERSION=dev
ARG COMMIT=dev

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Module graph first so layer caching is friendly to non-dep changes.
COPY go.mod go.sum* ./
RUN go mod download

# Source.
COPY . .

# Static, stripped, reproducible-ish build. CGO_ENABLED=0 keeps the binary
# distroless-static-compatible. GOOS/GOARCH come from the buildx target.
# -X main.Version / -X main.Commit pin the runtime --version output to
# the build args (#210).
ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
        -trimpath \
        -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT}" \
        -o /out/fhir-subs \
        ./cmd/fhir-subs

# OP #154 AC #3: build the demo CLIs into the same image so the
# documented walkthrough's `./demo-publisher` and `./demo-subscriber`
# work without a host Go toolchain. The README claimed these were
# baked in; before this change only fhir-subs was. The demo
# docker-compose.yml runs both as one-shot / long-running services
# with explicit entrypoint overrides.
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/demo-publisher \
        ./cmd/demo-publisher
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/demo-subscriber \
        ./cmd/demo-subscriber

# ---- Runtime stage --------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

LABEL org.opencontainers.image.title="fhir-ehr-subscriptions-service"
LABEL org.opencontainers.image.description="FHIR Subscriptions server bridging FHIR Subscriptions on the subscriber side and EHR systems on the back side."
LABEL org.opencontainers.image.source="https://github.com/bzimbelman/fhir-ehr-subscriptions-service"
LABEL org.opencontainers.image.licenses="Apache-2.0"
LABEL org.opencontainers.image.vendor="fhir-ehr-subscriptions-service"

COPY --from=build /out/fhir-subs /fhir-subs
# OP #154 AC #3: ship the demo CLIs alongside the bridge so the
# documented walkthrough works from the same image (no host toolchain
# required). Production deployments do not invoke these.
COPY --from=build /out/demo-publisher /demo-publisher
COPY --from=build /out/demo-subscriber /demo-subscriber

USER nonroot:nonroot

# 8443 = Subscriptions API HTTPS; 8081 = probes; 2575 = MLLP listener (one of N).
EXPOSE 8443 8081 2575

ENTRYPOINT ["/fhir-subs"]
