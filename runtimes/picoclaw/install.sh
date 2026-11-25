#!/usr/bin/env bash
# runtimes/picoclaw/install.sh — installer for the PicoClaw agentic backend.
set -euo pipefail

# Tee all output to a log under /root/.picoclaw (persistent rootfs), NOT /var/log — on these boards
# /var/log is a volatile zram mount wiped on reboot, which would lose the install log exactly when
# you need it.
PICO_LOG="${PICO_LOG:-/root/.picoclaw/install.log}"
mkdir -p "$(dirname "$PICO_LOG")"
exec > >(tee -a "$PICO_LOG") 2>&1
echo "[install-picoclaw] ===== install start $(date -u '+%Y-%m-%dT%H:%M:%SZ') (log: $PICO_LOG) ====="

PICO_BIN="/usr/local/bin/picoclaw"
PICO_DIR="/root/.picoclaw"
PICO_CONFIG="$PICO_DIR/config.json"

PICO_VERSION="${PICO_VERSION:-v0.3.1-fixvision}"
PICO_REPO="${PICO_REPO:-autonomous-ai/picoclaw}"

# Third-party downloads try the CDN mirror first, then the vendor.
OS_MIRROR_BASE="${OS_MIRROR_BASE:-https://cdn.autonomous.ai/os/mirror}"
# sha256 of the files this installer may take from the mirror ("<path> <sha256>"),
# the same values as MIRROR_PINS in software-update. A file without a pin comes
# from its vendor as before; a pinned file must match from the mirror or the vendor.
MIRROR_PINS='
yq/v4.46.1/yq_linux_arm64 4ab0b301059348d671fc1833e99903c1fecc7ca287ac131f72dca0eb9a6ba87a
yq/v4.46.1/yq_linux_amd64 c0eb42f6fbf928f0413422967983dcdf9806cc4dedc9394edc60c0dfb4a98529
picoclaw/v0.3.1-fixvision/picoclaw-linux-arm64 4bd0159fd02ca108b5c21bf08f6a044c8678f24c6d4a38ace2cadfb3ac9cffde
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

echo "[install-picoclaw] prerequisites (jq, yq, curl)"
apt-get update || true
apt-get install -y jq curl || true
# yq is required by the presync hook's .security.yml (YAML) edits.
if ! command -v yq >/dev/null 2>&1; then
  case "$(uname -m)" in
    x86_64)        YQ_BIN="yq_linux_amd64" ;;
    aarch64|arm64) YQ_BIN="yq_linux_arm64" ;;
    armv7l|armv6l) YQ_BIN="yq_linux_arm" ;;
    *) echo "[install-picoclaw] ERROR: unsupported arch $(uname -m) for yq"; exit 1 ;;
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

echo "[install-picoclaw] install PicoClaw binary ${PICO_VERSION}"
case "$(uname -m)" in
  aarch64|arm64) PICO_ASSET="picoclaw-linux-arm64" ;;
  x86_64)        PICO_ASSET="picoclaw-linux-amd64" ;;
  *) echo "[install-picoclaw] ERROR: unsupported arch $(uname -m) for picoclaw"; exit 1 ;;
esac
PICO_URL="https://github.com/${PICO_REPO}/releases/download/${PICO_VERSION}/${PICO_ASSET}"
PICO_TMP="$(mktemp)"
echo "[install-picoclaw] downloading ${PICO_URL}"
PICO_SHA256="$(mirror_pin "picoclaw/${PICO_VERSION}/${PICO_ASSET}")"
if [ -n "$PICO_SHA256" ]; then
  fetch_pinned "$PICO_TMP" "$PICO_SHA256" "${OS_MIRROR_BASE}/picoclaw/${PICO_VERSION}/${PICO_ASSET}" "$PICO_URL"
else
  curl -fsSL "$PICO_URL" -o "$PICO_TMP"
fi
install -m 0755 "$PICO_TMP" "$PICO_BIN"
rm -f "$PICO_TMP"
if [ ! -x "$PICO_BIN" ]; then
  echo "[install-picoclaw] ERROR: picoclaw not found at $PICO_BIN after install" >&2
  exit 1
fi
"$PICO_BIN" --version || true

# onboard creates /root/.picoclaw itself (workspace + a baseline config.json / .security.yml) — no
# explicit mkdir needed.
echo "[install-picoclaw] onboard (create workspace + baseline config) if absent"
if [ ! -f "$PICO_CONFIG" ]; then
  HOME=/root "$PICO_BIN" onboard || {
    echo "[install-picoclaw] ERROR: picoclaw onboard failed" >&2
    exit 1
  }
else
  echo "[install-picoclaw] config.json already present — skipping onboard"
fi

# OpenClaw persona/memory/skill import (`picoclaw migrate --workspace-only --force`) is owned by the
# presync hook now — it runs the migrate when the .openclaw-migrated marker is absent (first install
# OR after a factory reset wiped /root/.picoclaw), then asserts the model/channel config on top.

# Model + channel wiring (config.json agents.defaults/model_list/channel_list and .security.yml
# api_keys/channel tokens) is owned ENTIRELY by the presync hook, NOT patched here.
PRESYNC_HOOK="/usr/local/bin/runtime-picoclaw-presync"
if [ -x "$PRESYNC_HOOK" ]; then
  echo "[install-picoclaw] migrate + patch model/channel config now (via $PRESYNC_HOOK)"
  "$PRESYNC_HOOK" \
    || echo "[install-picoclaw] WARN: presync failed (config.json missing? non-fatal — switch-runtime retries on next switch)"
else
  echo "[install-picoclaw] WARN: $PRESYNC_HOOK absent — os-server did not materialize it (standalone/offline run?); PicoClaw model/channel config NOT set"
fi

echo "[install-picoclaw] write systemd unit picoclaw.service"
cat >/etc/systemd/system/picoclaw.service <<UNIT
[Unit]
Description=PicoClaw agent gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Environment=HOME=/root
WorkingDirectory=/root/.picoclaw
ExecStart=/usr/local/bin/picoclaw gateway
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
echo "[install-picoclaw] enable + start picoclaw.service"
systemctl enable --now picoclaw.service
systemctl status picoclaw.service --no-pager || true

echo "[install-picoclaw] declare verify hook for switch-runtime (command -v picoclaw)"
mkdir -p /usr/local/lib/os-runtimes/picoclaw
cat >/usr/local/lib/os-runtimes/picoclaw/verify <<'VERIFY'
#!/usr/bin/env bash
command -v picoclaw >/dev/null 2>&1
VERIFY
chmod +x /usr/local/lib/os-runtimes/picoclaw/verify

echo "[install-picoclaw] done — picoclaw gateway installed + started (picoclaw.service)."
echo "[install-picoclaw] ===== install finished $(date -u '+%Y-%m-%dT%H:%M:%SZ') ====="
