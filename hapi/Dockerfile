# syntax=docker/dockerfile:1.7
#
# Derived HAPI FHIR image with the subscription-service auth interceptors
# layered on top.
#
# Build:
#   docker build -t subscription-service/hapi:dev hapi/
#
# Run via deploy/docker/docker-compose.yml; the compose file pins the image
# name to subscription-service/hapi:dev and provides the
# SUBSCRIPTION_SERVICE_AUTH_* environment variables.
#
# Stage 1 builds the auth JAR using a Maven + JDK 17 image. Stage 2 takes
# the upstream hapiproject/hapi:v7.6.0 image (a distroless JRE with
# main.war pre-installed) and copies the JAR into /app/extra-classes/.
# The upstream entrypoint already runs PropertiesLauncher with
# -Dloader.path=main.war!/WEB-INF/classes/,main.war!/WEB-INF/,/app/extra-classes
# so any JAR placed in /app/extra-classes/ is picked up automatically and
# its Spring Boot auto-configuration (declared via
# META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports)
# is detected by the HAPI starter's @SpringBootApplication scan.
#
# Ticket: #359.

# --- Stage 1: build the auth jar --------------------------------------------
FROM maven:3.9.9-eclipse-temurin-17 AS build

WORKDIR /work

# Optional corporate-MITM CA injection. Off by default. To use:
#   cp ~/cz/.netskope-ca/full-chain.pem hapi/ca-cert/cert.pem
#   docker build -t subscription-service/hapi:dev hapi/
# Mirrors the pattern in ipf-app/Dockerfile so devs use the same workflow.
# Files in /usr/local/share/ca-certificates are picked up by Debian's
# update-ca-certificates AND by Java's `cacerts` (via the keytool import).
COPY ca-cert/ /tmp/ca-cert/
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

# Cache the dependency layer first: copy only the pom, resolve, then copy
# sources. This way edits to source files don't bust the dep cache.
COPY auth/pom.xml ./pom.xml
RUN --mount=type=cache,target=/root/.m2 \
    mvn -B -q -DskipTests dependency:go-offline || true

COPY auth/src ./src
RUN --mount=type=cache,target=/root/.m2 \
    mvn -B -q -DskipTests package

# --- Stage 2: layer onto upstream HAPI --------------------------------------
FROM hapiproject/hapi:v7.6.0 AS runtime

# /app/extra-classes is on the PropertiesLauncher loader path already.
# Copying the JAR there is sufficient — no entrypoint changes needed.
# The upstream image runs as UID 65532; the parent stage's chown happens
# automatically because COPY --from preserves Docker's default 0:0 only
# until the runtime image's USER directive applies. For a JAR that the
# JVM only needs to read, world-readable (the default) is fine.
COPY --from=build /work/target/subscription-service-hapi-auth-*.jar \
     /app/extra-classes/subscription-service-hapi-auth.jar
