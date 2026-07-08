#!/usr/bin/env bash
# fetch-igs.sh — download IG package tarballs into hapi/igs/ and matchbox/igs/.
#
# Idempotent: files that already exist on disk are left alone. Run any time
# you bring up a fresh checkout, or to refresh after pinning a new version
# in hapi/application.yaml.
#
# Environment overrides:
#   REGISTRY_URL   FHIR package registry base URL (default: packages.fhir.org)
#   FORCE          If non-empty, re-download even if the file exists.

set -euo pipefail

REGISTRY_URL="${REGISTRY_URL:-https://packages.fhir.org}"
FORCE="${FORCE:-}"

# Resolve repo root regardless of where the script is invoked from.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

HAPI_IGS_DIR="${REPO_ROOT}/hapi/igs"
MATCHBOX_IGS_DIR="${REPO_ROOT}/matchbox/igs"

mkdir -p "${HAPI_IGS_DIR}" "${MATCHBOX_IGS_DIR}"

# Each entry: <dest-dir>|<package-name>|<version>
# Add to this list when a new IG is needed.
PACKAGES=(
  "${HAPI_IGS_DIR}|hl7.fhir.us.core|7.0.0"
  # NOTE: the R4 variant (.r4 suffix) of the Subscriptions Backport IG.
  # The unsuffixed `hl7.fhir.uv.subscriptions-backport` declares R4B
  # (fhirVersion 4.3.0) and won't install on HAPI configured for R4 (4.0.1).
  "${HAPI_IGS_DIR}|hl7.fhir.uv.subscriptions-backport.r4|1.1.0"
  # HL7 v2-to-FHIR IG for Matchbox (ticket #361). The published v1.0.0 ships
  # ConceptMaps describing the message-level v2 → FHIR mapping; executable
  # StructureMaps for $transform on raw ER7 are NOT in this version of the
  # IG (the build.fhir.org continuous build has WIP StructureMaps but no
  # released package yet). The interface engine calls Matchbox $transform with the
  # canonical URL "http://hl7.org/fhir/uv/v2mappings/StructureMap/ADT_A01".
  # Operators should drop a project-owned or community-supplied StructureMap
  # into matchbox/maps/, or bump this pin once HL7 publishes the executable
  # maps. Until then ADT^A01 traffic gets ACK AE — by design, surfacing the
  # missing dependency rather than silently dropping the message.
  "${MATCHBOX_IGS_DIR}|hl7.fhir.uv.v2mappings|1.0.0"
)

download_one() {
  local dest_dir="$1"
  local name="$2"
  local version="$3"
  local out_file="${dest_dir}/${name}-${version}.tgz"

  if [[ -f "${out_file}" && -z "${FORCE}" ]]; then
    echo "skip   ${name}@${version} (already at ${out_file#"${REPO_ROOT}"/})"
    return 0
  fi

  local url="${REGISTRY_URL}/${name}/${version}"
  echo "fetch  ${name}@${version}  <-  ${url}"

  # --fail to error on 4xx/5xx, -L to follow redirects, -S so the error prints,
  # -s to suppress progress noise. We write to a tempfile then mv so a failed
  # download never leaves a half-written package on disk.
  local tmp
  tmp="$(mktemp "${out_file}.XXXXXX")"
  if curl --fail --location --show-error --silent -o "${tmp}" "${url}"; then
    # Sanity: must be a real gzip tarball, not an HTML error page.
    if ! file "${tmp}" | grep -q gzip; then
      echo "ERROR  ${name}@${version}: ${url} did not return a gzip tarball" >&2
      rm -f "${tmp}"
      return 1
    fi
    mv "${tmp}" "${out_file}"
    # HAPI runs as a non-root user inside the container and reads these via
    # a bind mount; the file must be world-readable. `mktemp` creates files
    # mode 600 by default, so without this chmod the IG load fails at boot
    # with "HAPI-2031: Error loading file:///app/igs/...".
    chmod 644 "${out_file}"
    echo "ok     ${name}@${version} -> ${out_file#"${REPO_ROOT}"/} ($(wc -c <"${out_file}") bytes)"
  else
    rm -f "${tmp}"
    echo "ERROR  ${name}@${version}: download failed from ${url}" >&2
    return 1
  fi
}

failures=0
for entry in "${PACKAGES[@]}"; do
  IFS='|' read -r dest name version <<<"${entry}"
  if ! download_one "${dest}" "${name}" "${version}"; then
    failures=$((failures + 1))
  fi
done

if (( failures > 0 )); then
  echo
  echo "${failures} package(s) failed to download." >&2
  exit 1
fi

echo
echo "All IG packages present:"
ls -la "${HAPI_IGS_DIR}"/*.tgz 2>/dev/null || echo "  (hapi/igs/ empty)"
ls -la "${MATCHBOX_IGS_DIR}"/*.tgz 2>/dev/null || echo "  (matchbox/igs/ empty — that's expected for now; the v2-to-FHIR IG comes in a later ticket)"
