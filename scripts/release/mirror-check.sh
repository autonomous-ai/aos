#!/usr/bin/env bash
# Sourced by release scripts that publish a version devices may take from the
# CDN mirror. Devices use a mirrored file only when its sha256 is pinned in
# MIRROR_PINS (scripts/provision/software-update); such a file must already be
# on the mirror, so the release stops here instead of shipping a pin the mirror
# cannot serve. Needs ota-config.sh sourced first.

MIRROR_PINS_FILE="${MIRROR_PINS_FILE:-${RELEASE_DIR}/../provision/software-update}"

# mirror_repo_pin <path>: the sha256 pinned for a mirrored file, or nothing.
mirror_repo_pin() {
  awk -v k="$1" "/^MIRROR_PINS='/ { on = 1; next } on && /^'/ { exit } on && \$1 == k { print \$2; exit }" \
    "$MIRROR_PINS_FILE"
}

# mirror_require <hint> <path>...: fail unless the CDN mirror serves every
# pinned <path>. <hint> is the command that mirrors them. Unpinned paths only
# print a note: devices download those from the vendor.
mirror_require() {
  local hint="$1" base key missing=()
  shift
  base="${MIRROR_CHECK_BASE:-$(ota_public_url "${BUCKET_PREFIX}/mirror")}"
  for key in "$@"; do
    if [ -z "$(mirror_repo_pin "$key")" ]; then
      echo "Note: no sha256 pin for mirror/${key}; devices download it from the vendor." \
        "To serve it from the mirror, add the line from 'scripts/release/upload-mirror.sh --digests' to MIRROR_PINS." >&2
      continue
    fi
    curl -fsSI --max-time 30 -o /dev/null "${base}/${key}" || missing+=("$key")
  done
  [ "${#missing[@]}" -eq 0 ] && return 0
  echo "ERROR: these pinned files are not on the CDN mirror (${base}/):" >&2
  printf '  %s\n' "${missing[@]}" >&2
  echo "Mirror them first with: ${hint}" >&2
  return 1
}
