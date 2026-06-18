# syntax=docker/dockerfile:1.7

# ---- Build stage ----------------------------------------------------------
# BUILDPLATFORM is the platform of the build host; TARGETOS / TARGETARCH are
# the platform we are building for. Combined with `docker buildx build
# --platform linux/amd64,linux/arm64`, this produces a multi-arch image with
# one native-host build per arch (cross-compiled by the Go toolchain).
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Module graph first so layer caching is friendly to non-dep changes.
COPY go.mod go.sum* ./
RUN go mod download

# Source.
COPY . .

# Static, stripped, reproducible-ish build. CGO_ENABLED=0 keeps the binary
# distroless-static-compatible. GOOS/GOARCH come from the buildx target.
ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/fhir-subs \
        ./cmd/fhir-subs

# ---- Runtime stage --------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

LABEL org.opencontainers.image.title="fhir-ehr-subscriptions-service"
LABEL org.opencontainers.image.description="FHIR Subscriptions server bridging FHIR Subscriptions on the subscriber side and EHR systems on the back side."
LABEL org.opencontainers.image.source="https://github.com/bzimbelman/fhir-ehr-subscriptions-service"
LABEL org.opencontainers.image.licenses="Apache-2.0"
LABEL org.opencontainers.image.vendor="fhir-ehr-subscriptions-service"

COPY --from=build /out/fhir-subs /fhir-subs

USER nonroot:nonroot

# 8443 = Subscriptions API HTTPS; 8081 = probes; 2575 = MLLP listener (one of N).
EXPOSE 8443 8081 2575

ENTRYPOINT ["/fhir-subs"]
