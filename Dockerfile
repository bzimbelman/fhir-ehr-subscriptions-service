# syntax=docker/dockerfile:1.7

# ---- Build stage ----------------------------------------------------------
FROM golang:1.22-alpine AS build

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Module graph first so layer caching is friendly to non-dep changes.
COPY go.mod go.sum* ./
RUN go mod download || true

# Source.
COPY . .

# Static, stripped, reproducible-ish build.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/fhir-subs \
        ./cmd/fhir-subs

# ---- Runtime stage --------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

LABEL org.opencontainers.image.title="fhir-subscriptions-foss"
LABEL org.opencontainers.image.description="FHIR Subscriptions server bridging FHIR Subscriptions on the subscriber side and EHR systems on the back side."
LABEL org.opencontainers.image.source="https://github.com/fhir-subscriptions-foss/fhir-subs"
LABEL org.opencontainers.image.licenses="Apache-2.0"
LABEL org.opencontainers.image.vendor="fhir-subscriptions-foss"

COPY --from=build /out/fhir-subs /fhir-subs

USER nonroot:nonroot

# 8443 = Subscriptions API HTTPS; 8081 = probes; 2575 = MLLP listener (one of N).
EXPOSE 8443 8081 2575

ENTRYPOINT ["/fhir-subs"]
