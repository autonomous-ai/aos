#!/usr/bin/env bash
# Mirror the pinned third-party tools that device updates download onto the OTA
# storage under <prefix>/mirror/, so devices that cannot reach the vendors fetch
# them from the CDN (software-update and runtimes/*/install.sh try the mirror
# first). Each file is downloaded from its vendor and copied unchanged.
#
# Versions come from the published OTA metadata (codex, picoclaw, opencode,
# hermes) and from the pins in software-update (uv, Node), so the mirror always
# matches what devices will ask for. Run again after publishing a new version.
#
# Devices use a mirrored file only when its sha256 is pinned in the repo
# (MIRROR_PINS in scripts/provision/software-update, and the pins in
# runtimes/*/install.sh). This script checks every download against an existing
# pin and prints the pin lines for new versions, so a version bump and its pins
# land in the same change.
#
# Usage: scripts/release/upload-mirror.sh [--dry-run | --digests]
#   --dry-run  print every source URL and destination; download and upload nothing.
#   --digests  download and print the pin lines; upload nothing.
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ota-config.sh"
source "${RELEASE_DIR}/ota-metadata.sh"

MODE=upload
case "${1:-}" in
  --dry-run) MODE=dry-run ;;
  --digests) MODE=digests ;;
  "") ;;
  *) echo "Usage: $0 [--dry-run | --digests]" >&2; exit 1 ;;
esac

UPDATER="${ROOT_DIR}/scripts/provision/software-update"
pin() { sed -n "s/^$1=\"\${$1:-\(.*\)}\"\$/\1/p" "$UPDATER" | head -1; }
UV_PIN="${UV_PIN:-$(pin HAL_UV_PIN)}"
NODE_DEB="${NODE_DEB:-$(pin NODE_MIRROR_DEB)}"
YQ_VERSION="${YQ_VERSION:-v4.46.1}"
[[ -n "$UV_PIN" && -n "$NODE_DEB" ]] || { echo "ERROR: cannot read HAL_UV_PIN / NODE_MIRROR_DEB from $UPDATER" >&2; exit 1; }

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# Read versions from the public metadata (no credentials needed).
curl -fsSL -H "Cache-Control: no-cache" -o "$WORK/envelope.json" "$(ota_public_url "${BUCKET_PREFIX}/ota/metadata.json")"
ota_metadata_unpack "$WORK/envelope.json" "$WORK/metadata.json"
meta() { jq -r "$1 // empty" "$WORK/metadata.json"; }
CODEX_VERSION="${CODEX_VERSION:-$(meta .codex.version)}"
PICO_VERSION="${PICO_VERSION:-$(meta .picoclaw.version)}"
OPENCODE_VERSION="${OPENCODE_VERSION:-$(meta .opencode.version)}"
HERMES_COMMIT="${HERMES_COMMIT:-$(meta .hermes.commit)}"

MIRROR="${BUCKET_PREFIX}/mirror"
ENTRIES=()
add() { ENTRIES+=("$1|$2"); }  # <vendor url>|<object path under the mirror>

for triple in aarch64-unknown-linux-gnu x86_64-unknown-linux-gnu; do
  add "https://github.com/astral-sh/uv/releases/download/${UV_PIN}/uv-${triple}.tar.gz" "uv/${UV_PIN}/uv-${triple}.tar.gz"
done
add "https://deb.nodesource.com/node_24.x/pool/main/n/nodejs/${NODE_DEB}" "node/${NODE_DEB}"
add "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_arm64" "yq/${YQ_VERSION}/yq_linux_arm64"
add "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_amd64" "yq/${YQ_VERSION}/yq_linux_amd64"
# Versions in the metadata (updates) and the pins in runtimes/*/install.sh (first install).
install_pin() { sed -n "s/^$2=\"\${$2:-\(.*\)}\"\$/\1/p" "${ROOT_DIR}/runtimes/$1/install.sh" | head -1; }
CODEX_PIN="$(install_pin codex CODEX_VERSION)"
for version in $(printf '%s\n' "$CODEX_VERSION" "${CODEX_PIN#rust-v}" | sort -u); do
  for asset in codex-aarch64-unknown-linux-musl.tar.gz codex-x86_64-unknown-linux-musl.tar.gz; do
    add "https://github.com/openai/codex/releases/download/rust-v${version}/${asset}" "codex/${version}/${asset}"
  done
done
for version in $(printf '%s\n' "$PICO_VERSION" "$(install_pin picoclaw PICO_VERSION)" | sort -u); do
  add "https://github.com/autonomous-ai/picoclaw/releases/download/${version}/picoclaw-linux-arm64" "picoclaw/${version}/picoclaw-linux-arm64"
done
for version in $(printf '%s\n' "$OPENCODE_VERSION" "$(install_pin opencode OPENCODE_VERSION)" | sort -u); do
  for asset in opencode-linux-arm64.tar.gz opencode-linux-x64.tar.gz; do
    add "https://github.com/anomalyco/opencode/releases/download/v${version}/${asset}" "opencode/${version}/${asset}"
  done
done
if [[ -n "$HERMES_COMMIT" ]]; then
  add "https://raw.githubusercontent.com/NousResearch/hermes-agent/${HERMES_COMMIT}/scripts/install.sh" "hermes/${HERMES_COMMIT}/install.sh"
fi

# repo_pin <key>: the sha256 pinned in software-update for <key>, or nothing.
repo_pin() { awk -v k="$1" '$1 == k { print $2; exit }' "$UPDATER"; }

PINS=()
for entry in "${ENTRIES[@]}"; do
  source_url="${entry%%|*}"
  key="${entry#*|}"
  object="${MIRROR}/${key}"
  if [[ "$MODE" == dry-run ]]; then
    printf '%s\n  -> gs://%s/%s\n  served at %s\n  pinned: %s\n' "$source_url" "$GCS_BUCKET" "$object" \
      "$(ota_public_url "$object")" "$(repo_pin "$key" || true)"
    continue
  fi
  file="$WORK/$(basename "$object")"
  echo "========== ${source_url} ==========" >&2
  curl -fsSL --retry 3 -o "$file" "$source_url"
  digest=$(ota_artifact_sha256 "$file")
  pinned=$(repo_pin "$key" || true)
  if [[ -n "$pinned" && "$pinned" != "$digest" ]]; then
    echo "ERROR: ${source_url} has sha256 ${digest}, the repo pins ${pinned}; refusing to mirror it" >&2
    exit 1
  fi
  PINS+=("${key} ${digest}")
  if [[ "$MODE" == upload ]]; then
    gsutil -h "Cache-Control:public, max-age=31536000, immutable" cp "$file" "gs://${GCS_BUCKET}/${object}"
  fi
  rm -f "$file"
done
if [[ "$MODE" != dry-run ]]; then
  echo "# Pin lines (MIRROR_PINS in scripts/provision/software-update; runtimes/*/install.sh for first installs):"
  printf '%s\n' "${PINS[@]}"
fi
[[ "$MODE" != upload ]] || echo "Done: ${#ENTRIES[@]} files under $(ota_public_url "$MIRROR")/" >&2
