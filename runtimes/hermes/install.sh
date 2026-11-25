#!/usr/bin/env bash
# Hermes backend installer, run by switch-runtime on first switch; config.yaml is owned by presync.
# The gateway must listen on 127.0.0.1:8642 (runtimes/hermes/constants.go BaseURL).
set -euo pipefail

# Log to persistent /root/.hermes: /var/log is volatile zram (log2ram) on these boards.
HERMES_LOG="${HERMES_LOG:-/root/.hermes/install.log}"
mkdir -p "$(dirname "$HERMES_LOG")"
exec > >(tee -a "$HERMES_LOG") 2>&1
echo "[install-hermes] ===== install start $(date -u '+%Y-%m-%dT%H:%M:%SZ') (log: $HERMES_LOG) ====="

HERMES_BIN="/usr/local/bin/hermes"
HERMES_DIR="/root/.hermes"
ENV_FILE="$HERMES_DIR/.env"

# Must equal runtimes/hermes/constants.go APIKey (401 on every turn otherwise).
HERMES_API_SERVER_KEY="hermes-local-api-key"

# Third-party downloads try the CDN mirror first, then the vendor.
OS_MIRROR_BASE="${OS_MIRROR_BASE:-https://cdn.autonomous.ai/os/mirror}"
# sha256 of the files this installer may take from the mirror ("<path> <sha256>"),
# the same values as MIRROR_PINS in software-update. A file without a pin comes
# from its vendor as before; a pinned file must match from the mirror or the vendor.
MIRROR_PINS='
yq/v4.46.1/yq_linux_arm64 4ab0b301059348d671fc1833e99903c1fecc7ca287ac131f72dca0eb9a6ba87a
yq/v4.46.1/yq_linux_amd64 c0eb42f6fbf928f0413422967983dcdf9806cc4dedc9394edc60c0dfb4a98529
'
mirror_pin() { printf '%s\n' "$MIRROR_PINS" | awk -v k="$1" '$1 == k { print $2; exit }'; }
# fetch_pinned <dest> <sha256> <url>...: the first URL whose download matches the digest.
fetch_pinned() {
  local dest="$1" digest="$2" url
  shift 2
  for url in "$@"; do
    if curl -fsSL "$url" -o "$dest" && echo "$digest  $dest" | sha256sum -c - >/dev/null 2>&1; then
      return 0
    fi
    rm -f "$dest"
  done
  return 1
}

echo "[install-hermes] prerequisites (jq, yq, curl)"
apt-get update || true
apt-get install -y jq curl || true
if ! command -v yq >/dev/null 2>&1; then
  case "$(uname -m)" in
    x86_64)        YQ_BIN="yq_linux_amd64" ;;
    aarch64|arm64) YQ_BIN="yq_linux_arm64" ;;
    armv7l|armv6l) YQ_BIN="yq_linux_arm" ;;
    *) echo "[install-hermes] ERROR: unsupported arch $(uname -m) for yq"; exit 1 ;;
  esac
  YQ_URL="https://github.com/mikefarah/yq/releases/download/v4.46.1/${YQ_BIN}"
  YQ_SHA256="$(mirror_pin "yq/v4.46.1/${YQ_BIN}")"
  if [ -n "$YQ_SHA256" ]; then
    fetch_pinned /usr/local/bin/yq "$YQ_SHA256" "${OS_MIRROR_BASE}/yq/v4.46.1/${YQ_BIN}" "$YQ_URL"
  else
    curl -fsSL "$YQ_URL" -o /usr/local/bin/yq
  fi
  chmod +x /usr/local/bin/yq
fi

echo "[install-hermes] install Hermes CLI (staged — skip node-deps browser tools)"
# Run the upstream installer stage by stage, skipping `node-deps`: its npm native builds hang on ARM.
HERMES_INSTALLER="$(mktemp)"
curl -fsSL https://hermes-agent.nousresearch.com/install.sh -o "$HERMES_INSTALLER"
for stage in prerequisites repository venv python-deps path config; do
  echo "[install-hermes] hermes installer stage: ${stage}"
  bash "$HERMES_INSTALLER" --stage "$stage" --non-interactive
done
rm -f "$HERMES_INSTALLER"
# Mark as a git install so a later `hermes update` recognizes it.
echo "git" >/usr/local/lib/hermes-agent/.install_method 2>/dev/null || true
if [ ! -x "$HERMES_BIN" ]; then
  echo "[install-hermes] ERROR: hermes not found at $HERMES_BIN after install" >&2
  command -v hermes || true
  exit 1
fi
"$HERMES_BIN" --version || true

echo "[install-hermes] stop openclaw before migrating (avoid racing its state)"
for attempt in 1 2 3; do
  systemctl stop openclaw 2>/dev/null || true
  if ! systemctl is-active --quiet openclaw; then
    echo "[install-hermes] openclaw stopped (attempt ${attempt}/3)"
    break
  fi
  echo "[install-hermes] WARN: openclaw still active after attempt ${attempt}/3"
  [ "$attempt" -eq 3 ] && echo "[install-hermes] WARN: openclaw still active after 3 attempts — continuing anyway"
  sleep 1
done


mkdir -p "$HERMES_DIR"
touch "$ENV_FILE"

echo "[install-hermes] seed $ENV_FILE (upsert — other vars preserved)"
for k in API_SERVER_ENABLED API_SERVER_KEY API_SERVER_CORS_ORIGINS; do
  sed -i "/^${k}=/d" "$ENV_FILE"
done

[ -s "$ENV_FILE" ] && [ -n "$(tail -c1 "$ENV_FILE")" ] && printf '\n' >>"$ENV_FILE"
cat >>"$ENV_FILE" <<EOF
API_SERVER_ENABLED=true
API_SERVER_KEY=${HERMES_API_SERVER_KEY}
API_SERVER_CORS_ORIGINS=http://localhost:3000
EOF

# config.yaml wiring is owned by the presync hook (materialized by os-server) so an OTA can fix it.
PRESYNC_HOOK="/usr/local/bin/runtime-hermes-presync"
if [ -x "$PRESYNC_HOOK" ]; then
  echo "[install-hermes] ensure config.yaml structure + sync llm_* now (via $PRESYNC_HOOK)"
  "$PRESYNC_HOOK" \
    || echo "[install-hermes] WARN: presync failed (config.json missing? non-fatal — switch-runtime retries on next switch)"
else
  echo "[install-hermes] WARN: $PRESYNC_HOOK absent — os-server did not materialize it (standalone/offline run?); Hermes model config NOT set"
fi

echo "[install-hermes] install + start hermes gateway as a system service"
# `yes` gets SIGPIPE once install exits; under pipefail that would fail a successful install.
set +o pipefail
yes y | "$HERMES_BIN" gateway install --system --run-as-user root
set -o pipefail
# Keep optional gateway imports off the disk while HAL starts; the '-' prefix fails open.
HARDWARE_STARTUP_DIR=/etc/systemd/system/hermes-gateway.service.d
HARDWARE_STARTUP_DROPIN="$HARDWARE_STARTUP_DIR/20-hardware-startup.conf"
mkdir -p "$HARDWARE_STARTUP_DIR"
HARDWARE_STARTUP_TMP=$(mktemp "$HARDWARE_STARTUP_DIR/.hardware-startup-XXXXXX")
printf '[Service]\nExecStartPre=-/usr/local/bin/os-server --wait-hal-ready\n' >"$HARDWARE_STARTUP_TMP"
if cmp -s "$HARDWARE_STARTUP_TMP" "$HARDWARE_STARTUP_DROPIN"; then
  rm -f "$HARDWARE_STARTUP_TMP"
else
  chmod 0644 "$HARDWARE_STARTUP_TMP"
  mv -f "$HARDWARE_STARTUP_TMP" "$HARDWARE_STARTUP_DROPIN"
  systemctl daemon-reload
fi
"$HERMES_BIN" gateway start --system
"$HERMES_BIN" gateway status --system || true

echo "[install-hermes] declare unit name for switch-runtime (hermes-gateway)"
mkdir -p /usr/local/lib/os-runtimes/hermes
echo "hermes-gateway" >/usr/local/lib/os-runtimes/hermes/service

# Verify hook: switch-runtime reinstalls when this fails (orphaned unit, missing binary).
echo "[install-hermes] declare verify hook for switch-runtime (command -v hermes)"
cat >/usr/local/lib/os-runtimes/hermes/verify <<'VERIFY'
#!/usr/bin/env bash
command -v hermes >/dev/null 2>&1
VERIFY
chmod +x /usr/local/lib/os-runtimes/hermes/verify

echo "[install-hermes] done — hermes gateway installed + started as a system service (hermes-gateway.service)."
echo "[install-hermes] ===== install finished $(date -u '+%Y-%m-%dT%H:%M:%SZ') ====="
