# syntax=docker/dockerfile:1.7

# --- Stage 1: build ----------------------------------------------------------
# Pre-built Gradle 8.11.1 image on JDK 21 — using a pinned image avoids the
# gradle wrapper having to download its distribution at build time. The
# wrapper still works for local builds outside Docker.
FROM gradle:8.11.1-jdk21 AS build

WORKDIR /work

# Build context note: ticket #430 moved this image's build context to the
# REPO ROOT (was previously `interface-engine/`). The reason is the new
# multi-project Gradle setup: `interface-engine` now depends on
# `:plugins-spi` (a sibling Gradle module at the repo root), and the
# multi-project resolution requires the root `settings.gradle.kts` plus
# both subprojects' sources on disk. docker-compose / Helm builds that
# previously used `context: interface-engine/` were updated to
# `context: .` and `dockerfile: interface-engine/Dockerfile`. See
# `deploy/docker/docker-compose.yml` for the canonical form.

# Optional corporate-MITM CA injection. Off by default. To use:
#   cp ~/.netskope-ca/full-chain.pem interface-engine/ca-cert/cert.pem
#   docker build -t subscription-service/interface-engine:dev \
#       -f interface-engine/Dockerfile .
# The COPY is conditional: if ca-cert/cert.pem is absent the import is a no-op.
# The file may contain a CA chain (multiple PEM blocks); we split it and import
# each cert into the JVM truststore individually because `keytool -importcert`
# only reads the FIRST PEM block from a file. The OS truststore (`update-ca-
# certificates`) handles chains correctly on its own.
COPY interface-engine/ca-cert/ /tmp/ca-cert/
RUN if [ -s /tmp/ca-cert/cert.pem ]; then \
        cp /tmp/ca-cert/cert.pem /usr/local/share/ca-certificates/extra-ca.crt \
        && update-ca-certificates ; \
        awk '/-----BEGIN CERTIFICATE-----/{i++} {print > ("/tmp/cert-" i ".pem")}' /tmp/ca-cert/cert.pem ; \
        for f in /tmp/cert-*.pem ; do \
            alias="extra-ca-$(basename "$f" .pem)" ; \
            keytool -importcert -noprompt -trustcacerts \
                -alias "$alias" \
                -file "$f" \
                -keystore "$JAVA_HOME/lib/security/cacerts" \
                -storepass changeit ; \
        done ; \
        echo "Corporate CA(s) installed." ; \
    else \
        echo "No corporate CA; using default trust store." ; \
    fi

# Copy only files that influence dependency resolution first; the Gradle layer
# cache then survives unrelated source-code edits.
#   - Root settings + build files (multi-project metadata)
#   - Each subproject's build file
COPY settings.gradle.kts build.gradle.kts ./
COPY interface-engine/build.gradle.kts ./interface-engine/
COPY plugins-spi/build.gradle.kts ./plugins-spi/
# Built-in plugin modules (Epic #425 tickets #431-#435). Each is its own
# Gradle subproject referenced by the root `settings.gradle.kts` and
# pulled into the interface-engine runtime classpath via the
# `interface-engine/build.gradle.kts` dependencies. Their build files
# must be present before the dependency-resolution warm-up.
COPY plugins-builtin/hl7v2-mllp/build.gradle.kts ./plugins-builtin/hl7v2-mllp/
COPY plugins-builtin/audit-event-fhir/build.gradle.kts ./plugins-builtin/audit-event-fhir/
COPY plugins-builtin/observability-otel/build.gradle.kts ./plugins-builtin/observability-otel/
COPY plugins-builtin/fhir-polling/build.gradle.kts ./plugins-builtin/fhir-polling/
COPY plugins-builtin/profile-loader/build.gradle.kts ./plugins-builtin/profile-loader/
RUN --mount=type=cache,target=/root/.gradle gradle --no-daemon --version

# Now bring in the rest and build the executable jar.
COPY interface-engine/src ./interface-engine/src
COPY plugins-spi/src ./plugins-spi/src
COPY plugins-builtin/hl7v2-mllp/src ./plugins-builtin/hl7v2-mllp/src
COPY plugins-builtin/audit-event-fhir/src ./plugins-builtin/audit-event-fhir/src
COPY plugins-builtin/observability-otel/src ./plugins-builtin/observability-otel/src
COPY plugins-builtin/fhir-polling/src ./plugins-builtin/fhir-polling/src
COPY plugins-builtin/profile-loader/src ./plugins-builtin/profile-loader/src
RUN --mount=type=cache,target=/root/.gradle \
    gradle --no-daemon :interface-engine:clean :interface-engine:bootJar -x test

# --- Stage 2: runtime --------------------------------------------------------
# JRE 21 base. `-jammy` over `-alpine` because Camel MLLP / HAPI HL7v2 are not
# routinely tested against musl; sticking with glibc avoids subtle native bugs.
FROM eclipse-temurin:21-jre-jammy AS runtime

# curl for the HEALTHCHECK below.
RUN apt-get update \
 && apt-get install -y --no-install-recommends curl \
 && rm -rf /var/lib/apt/lists/*

# Run as non-root.
RUN useradd --system --uid 10001 --home /app --shell /usr/sbin/nologin app
WORKDIR /app

COPY --from=build /work/interface-engine/build/libs/*.jar /app/app.jar

# 8090 = Spring Boot HTTP / actuator. 2575 = MLLP listener.
EXPOSE 8090 2575

HEALTHCHECK --interval=15s --timeout=3s --start-period=30s --retries=4 \
    CMD curl --fail --silent http://localhost:8090/actuator/health || exit 1

USER app

ENTRYPOINT ["java", "-XX:+UseContainerSupport", "-jar", "/app/app.jar"]
