#!/usr/bin/env bash
# Shared GCS config sourced by the release scripts; env overrides win (`:=`).

RELEASE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${RELEASE_DIR}/../.." && pwd)"

: "${OTA_DEFAULT_BUCKET:=s3-autonomous-upgrade-3}"
: "${GCS_BUCKET:=$OTA_DEFAULT_BUCKET}"

: "${BUCKET_PREFIX:=os}"

# Public base URL written into metadata for published files. The CDN serves the
# default OTA storage byte for byte and is reachable where the storage host is
# not; any other bucket (staging, forks) keeps its storage URL.
if [[ -z "${OTA_PUBLIC_BASE:-}" ]]; then
  if [[ "$GCS_BUCKET" == "$OTA_DEFAULT_BUCKET" ]]; then
    OTA_PUBLIC_BASE="https://cdn.autonomous.ai"
  else
    OTA_PUBLIC_BASE="https://storage.googleapis.com/${GCS_BUCKET}"
  fi
fi

# ota_public_url <object-path>: the download URL for an object in GCS_BUCKET.
ota_public_url() {
  printf '%s/%s\n' "${OTA_PUBLIC_BASE%/}" "${1#/}"
}
