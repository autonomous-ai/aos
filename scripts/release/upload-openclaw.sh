#!/usr/bin/env bash
set -e

# Publish an openclaw version to OTA metadata (metadata only, no GCS artifact).
# Usage: ./scripts/release/upload-openclaw.sh <version_str>

if [[ -z "${1:-}" ]]; then
  echo "Usage: $0 <openclaw-version>" >&2
  echo "Example: $0 1.2.3" >&2
  exit 1
fi
VERSION="$1"

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ota-config.sh"
source "${RELEASE_DIR}/ota-metadata.sh"
METADATA_GCS="gs://${GCS_BUCKET}/${BUCKET_PREFIX}/ota/metadata.json"

METADATA_TMP=$(mktemp)
PAYLOAD_TMP=$(mktemp)
trap 'rm -f "$METADATA_TMP" "$PAYLOAD_TMP"' EXIT

if ! ota_metadata_fetch "$METADATA_GCS" "$METADATA_TMP" 2>/dev/null; then
  echo "Note: $METADATA_GCS not found — bootstrapping with empty object."
  printf '{}' > "$PAYLOAD_TMP"
else
  ota_metadata_unpack "$METADATA_TMP" "$PAYLOAD_TMP"
fi

# Tarball integrity from the default npm registry. Devices that install from
# another registry install only packages that match these values.
OC_INTEGRITY=$(npm view "openclaw@${VERSION}" dist.integrity --registry https://registry.npmjs.org/)
OC_DISCORD_INTEGRITY=$(npm view "@openclaw/discord@${VERSION}" dist.integrity --registry https://registry.npmjs.org/ 2>/dev/null || true)
OC_SLACK_INTEGRITY=$(npm view "@openclaw/slack@${VERSION}" dist.integrity --registry https://registry.npmjs.org/ 2>/dev/null || true)
[[ "$OC_INTEGRITY" == sha512-* ]] || { echo "ERROR: no sha512 integrity for openclaw@${VERSION}" >&2; exit 1; }

python3 - "$PAYLOAD_TMP" "$VERSION" "$(date '+%Y-%m-%d %H:%M:%S %z')" "$OC_INTEGRITY" "$OC_DISCORD_INTEGRITY" "$OC_SLACK_INTEGRITY" <<'PY'
import json
import sys

path, version, updated_at, integrity, discord, slack = sys.argv[1:7]
d = json.load(open(path))
oc = d.get("openclaw") if isinstance(d.get("openclaw"), dict) else {}
oc["version"] = version
oc["updated_at"] = updated_at
oc["integrity"] = integrity
oc["plugins"] = {name: value for name, value in (("@openclaw/discord", discord), ("@openclaw/slack", slack))
                 if value.startswith("sha512-")}
d["openclaw"] = oc
json.dump(d, open(path, "w"), indent=4)
PY

ota_metadata_sign "$PAYLOAD_TMP" "$METADATA_TMP"

ota_metadata_publish "$METADATA_TMP" "$METADATA_GCS"

echo "Updated $METADATA_GCS: openclaw.version = ${VERSION}"
