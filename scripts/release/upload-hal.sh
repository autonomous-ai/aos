#!/usr/bin/env bash
set -e

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ota-config.sh"
source "${RELEASE_DIR}/ota-metadata.sh"

HAL_DIR="${ROOT_DIR}/hal"
VERSION_FILE="${ROOT_DIR}/hal/${VERSION_FILE:-VERSION_HAL}"

command -v uv >/dev/null 2>&1 || { echo "Error: uv is required to validate the HAL lockfile" >&2; exit 1; }
uv lock --project "$HAL_DIR" --python 3.12 --check

if [[ -f "$VERSION_FILE" ]]; then
  version=$(cat "$VERSION_FILE" | tr -d '[:space:]')
  IFS='.' read -r major minor patch <<< "$version"
  patch=$((patch + 1))
  new_version="${major}.${minor}.${patch}"
  echo "$new_version" > "$VERSION_FILE"
  echo "========== Version bumped: ${version} -> ${new_version} =========="
else
  echo "1.0.0" > "$VERSION_FILE"
  new_version="1.0.0"
  echo "========== Version initialized: ${new_version} =========="
fi

ZIP_NAME="hal-${new_version}.zip"
ZIP_PATH="${ROOT_DIR}/${ZIP_NAME}"
GCS_PATH="${GCS_PATH:-${BUCKET_PREFIX}/ota/hal/${new_version}.zip}"

if [[ ! -d "$HAL_DIR" ]]; then
  echo "Error: hal directory not found at $HAL_DIR"
  exit 1
fi

echo "========== Zipping hal to ${ZIP_NAME} =========="
rm -f "$ZIP_PATH"
(cd "$HAL_DIR" && zip -r "$ZIP_PATH" . \
  -x ".venv/*" "__pycache__/*" "*/__pycache__/*" ".git/*" "*.pyc" \
  ".env" ".python-version" "test/*")

echo "========== Upload ${ZIP_NAME} to Google Cloud Storage (no-cache) =========="
gsutil -h "Cache-Control:no-cache, no-store, must-revalidate" cp "$ZIP_PATH" "gs://${GCS_BUCKET}/${GCS_PATH}"
ZIP_SHA256=$(ota_artifact_sha256 "$ZIP_PATH")

METADATA_PATH="${BUCKET_PREFIX}/ota/metadata.json"
METADATA_TMP=$(mktemp)
PAYLOAD_TMP=$(mktemp)
HAL_URL="${HAL_URL:-$(ota_public_url "${GCS_PATH}")}"

echo "========== Fetch metadata from gs://${GCS_BUCKET}/${METADATA_PATH} =========="
if ota_metadata_fetch "gs://${GCS_BUCKET}/${METADATA_PATH}" "$METADATA_TMP" 2>/dev/null; then
  ota_metadata_unpack "$METADATA_TMP" "$PAYLOAD_TMP"
else
  printf '{}' >"$PAYLOAD_TMP"
fi

# Offline wheelhouse for devices that cannot reach the package index (see
# docs/bootstrap-ota.md). HAL_WHEELHOUSE=auto (default) builds one only when the
# published wheelhouse was made from another uv.lock, =1 always, =0 never. In
# auto mode a failed build only warns: devices keep the online install.
HAL_WHEELHOUSE="${HAL_WHEELHOUSE:-auto}"
HAL_WHEELHOUSE_PLATFORM="${HAL_WHEELHOUSE_PLATFORM:-linux-aarch64-aec}"
WH_JSON=""
LOCK_SHA256=$(ota_artifact_sha256 "$HAL_DIR/uv.lock")
WH_PUBLISHED_LOCK=$(python3 - "$PAYLOAD_TMP" "$HAL_WHEELHOUSE_PLATFORM" <<'PY'
import json, sys
hal = json.load(open(sys.argv[1])).get('hal') or {}
print(((hal.get('wheelhouse') or {}).get(sys.argv[2]) or {}).get('lock_sha256', ''))
PY
)
if [[ "$HAL_WHEELHOUSE" == 1 || ( "$HAL_WHEELHOUSE" == auto && "$WH_PUBLISHED_LOCK" != "$LOCK_SHA256" ) ]]; then
  WH_OUT="${ROOT_DIR}/hal-wheelhouse-${HAL_WHEELHOUSE_PLATFORM}.tar.gz"
  WH_MACHINE="${HAL_WHEELHOUSE_PLATFORM#linux-}"
  WH_EXTRA="${WH_MACHINE#*-}"
  WH_MACHINE="${WH_MACHINE%%-*}"
  echo "========== Build HAL wheelhouse ${HAL_WHEELHOUSE_PLATFORM} =========="
  if uv run --no-project --python 3.12 --with packaging python3 "${RELEASE_DIR}/build_hal_wheelhouse.py" \
      --hal-dir "$HAL_DIR" --extra "$WH_EXTRA" --machine "$WH_MACHINE" --out "$WH_OUT"; then
    WH_SHA256=$(ota_artifact_sha256 "$WH_OUT")
    WH_GCS_PATH="${BUCKET_PREFIX}/ota/hal/wheelhouse/${HAL_WHEELHOUSE_PLATFORM}-${WH_SHA256}.tar.gz"
    gsutil -h "Cache-Control:public, max-age=31536000, immutable" cp "$WH_OUT" "gs://${GCS_BUCKET}/${WH_GCS_PATH}"
    WH_JSON=$(python3 -c 'import json, sys; print(json.dumps({sys.argv[1]: {"url": sys.argv[2], "sha256": sys.argv[3], "lock_sha256": sys.argv[4]}}))' \
      "$HAL_WHEELHOUSE_PLATFORM" "$(ota_public_url "$WH_GCS_PATH")" "$WH_SHA256" "$LOCK_SHA256")
  elif [[ "$HAL_WHEELHOUSE" == 1 ]]; then
    echo "Error: HAL wheelhouse build failed" >&2
    exit 1
  else
    echo "WARN: HAL wheelhouse build failed; devices without package index access cannot install this HAL" >&2
  fi
  rm -f "$WH_OUT"
fi

updated_metadata=$(python3 - "$PAYLOAD_TMP" "$new_version" "$HAL_URL" "$ZIP_SHA256" "$(date '+%Y-%m-%d %H:%M:%S %z')" "$WH_JSON" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
entry = data.get('hal') if isinstance(data.get('hal'), dict) else {}
entry.update({'version': sys.argv[2], 'url': sys.argv[3], 'sha256': sys.argv[4], 'updated_at': sys.argv[5]})
if sys.argv[6]:
    # Devices use a wheelhouse only when its lock_sha256 matches the HAL they install.
    wheelhouse = entry.get('wheelhouse') if isinstance(entry.get('wheelhouse'), dict) else {}
    wheelhouse.update(json.loads(sys.argv[6]))
    entry['wheelhouse'] = wheelhouse
# preserve existing min_version (staged-rollout floor); bump it via promote-ota.sh
data['hal'] = entry
print(json.dumps(data, indent=2))
PY
)

echo "$updated_metadata" > "$PAYLOAD_TMP"
ota_metadata_sign "$PAYLOAD_TMP" "$METADATA_TMP"
rm -f "$PAYLOAD_TMP"
echo "========== Upload metadata (hal: v${new_version}) =========="
ota_metadata_publish "$METADATA_TMP" "gs://${GCS_BUCKET}/${METADATA_PATH}"
rm -f "$METADATA_TMP"

rm -f "$ZIP_PATH"
echo "Done: gs://${GCS_BUCKET}/${GCS_PATH} (v${new_version})"
