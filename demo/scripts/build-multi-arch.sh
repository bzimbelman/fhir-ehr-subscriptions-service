#!/usr/bin/env bash
# OP #159: build the demo bridge image for both linux/amd64 and linux/arm64
# in one shot via `docker buildx bake`. Plain `docker compose build` only
# produces a multi-arch image when buildx is the active builder; this
# script makes the multi-arch path explicit and tooled.
#
# Usage:
#   demo/scripts/build-multi-arch.sh                # build into local docker
#   PUSH=1 IMAGE=ghcr.io/me/fhir-subs:demo \
#     demo/scripts/build-multi-arch.sh              # push to registry
#
# Requirements:
#   - docker >= 23 (buildx is included by default).
#   - The active builder MUST support multi-platform. Older Docker Desktop
#     and Docker Engine installs default to the legacy `default` builder
#     which only builds for the host's native arch. The script bootstraps a
#     buildx builder named `fhir-subs-multiarch` if buildx is missing one.
#   - When PUSH is set the IMAGE must be a fully-qualified registry ref.
#
# This script does NOT touch demo/docker-compose.yml. After it builds and
# loads the bridge image into the local docker, `docker compose up -d`
# uses the cached image without rebuilding (compose's `image:` line ties
# back to `fhir-subs:demo`).
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"

image="${IMAGE:-fhir-subs:demo}"
platforms="${PLATFORMS:-linux/amd64,linux/arm64}"
builder_name="${BUILDX_BUILDER:-fhir-subs-multiarch}"
push="${PUSH:-0}"

if ! command -v docker >/dev/null 2>&1; then
    echo "demo: docker is required" >&2
    exit 1
fi
if ! docker buildx version >/dev/null 2>&1; then
    echo "demo: docker buildx is required (Docker >= 23, or install the buildx CLI plugin)" >&2
    exit 1
fi

# Bootstrap a buildx builder that supports multi-platform if the current
# active builder does not. Re-running is idempotent.
if ! docker buildx inspect "${builder_name}" >/dev/null 2>&1; then
    echo "demo: creating buildx builder ${builder_name}" >&2
    docker buildx create --name "${builder_name}" --driver docker-container >/dev/null
fi
docker buildx use "${builder_name}"

# Local docker daemon cannot load a multi-arch manifest list. When PUSH=0
# the script builds + loads only the host arch; PUSH=1 builds the full
# multi-platform manifest and pushes it.
if [[ "${push}" = "1" ]]; then
    echo "demo: building ${image} for ${platforms} (push)" >&2
    docker buildx build \
        --platform "${platforms}" \
        --tag "${image}" \
        --file "${repo_root}/Dockerfile" \
        --push \
        "${repo_root}"
else
    host_arch="$(uname -m)"
    case "${host_arch}" in
        x86_64|amd64) host_platform="linux/amd64" ;;
        aarch64|arm64) host_platform="linux/arm64" ;;
        *) echo "demo: unsupported host arch ${host_arch}" >&2; exit 1 ;;
    esac
    echo "demo: building ${image} for ${host_platform} (load into local docker)" >&2
    echo "demo: set PUSH=1 IMAGE=<registry-ref> to publish ${platforms}" >&2
    docker buildx build \
        --platform "${host_platform}" \
        --tag "${image}" \
        --file "${repo_root}/Dockerfile" \
        --load \
        "${repo_root}"
fi

echo "demo: done. ${image} is ready." >&2
