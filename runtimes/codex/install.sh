#!/usr/bin/env bash
# runtimes/codex/install.sh — installer for the Codex agentic backend.
set -euo pipefail

# Tee all output to a log under /root/.codex (persistent rootfs), NOT /var/log — on these boards
# /var/log is a volatile zram mount wiped on reboot.
CODEX_LOG="${CODEX_LOG:-/root/.codex/install.log}"
mkdir -p "$(dirname "$CODEX_LOG")"
exec > >(tee -a "$CODEX_LOG") 2>&1
echo "[install-codex] ===== install start $(date -u '+%Y-%m-%dT%H:%M:%SZ') (log: $CODEX_LOG) ====="

export HOME=/root
CODEX_BIN="/usr/local/bin/codex"

# Pinned release; bump here and re-OTA os-server to upgrade.
CODEX_VERSION="${CODEX_VERSION:-rust-v0.142.5}"
CODEX_REPO="${CODEX_REPO:-openai/codex}"
# Third-party downloads try the CDN mirror first, then the vendor.
OS_MIRROR_BASE="${OS_MIRROR_BASE:-https://cdn.autonomous.ai/os/mirror}"
# sha256 of the files this installer may take from the mirror ("<path> <sha256>"),
# the same values as MIRROR_PINS in software-update. A file without a pin comes
# from its vendor as before; a pinned file must match from the mirror or the vendor.
MIRROR_PINS='
codex/0.142.5/codex-aarch64-unknown-linux-musl.tar.gz b18c75c49645918fae23beba0ab41c05f07941601510a2451ba97fe519573c38
codex/0.142.5/codex-x86_64-unknown-linux-musl.tar.gz cb933ec3cb61bf4b5fc88eecf5e6149829faa6172535b6ef0afb0154beb4aab8
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

echo "[install-codex] prerequisites (jq, curl)"
apt-get update || true
apt-get install -y jq curl || true

echo "[install-codex] install Codex CLI ${CODEX_VERSION}"
case "$(uname -m)" in
  aarch64|arm64) CODEX_ASSET="codex-aarch64-unknown-linux-musl.tar.gz" ;;
  x86_64)        CODEX_ASSET="codex-x86_64-unknown-linux-musl.tar.gz" ;;
  *) echo "[install-codex] ERROR: unsupported arch $(uname -m) for codex"; exit 1 ;;
esac
# Idempotency: skip the download when the pinned version is already installed.
WANT_VERSION="${CODEX_VERSION#rust-v}"
if command -v codex >/dev/null 2>&1 && [ "$(codex --version 2>/dev/null | awk '{print $2}')" = "$WANT_VERSION" ]; then
  echo "[install-codex] codex ${WANT_VERSION} already installed — skipping download"
else
  CODEX_URL="https://github.com/${CODEX_REPO}/releases/download/${CODEX_VERSION}/${CODEX_ASSET}"
  CODEX_TMP="$(mktemp -d)"
  echo "[install-codex] downloading ${CODEX_URL}"
  CODEX_SHA256="$(mirror_pin "codex/${WANT_VERSION}/${CODEX_ASSET}")"
  if [ -n "$CODEX_SHA256" ]; then
    fetch_pinned "$CODEX_TMP/$CODEX_ASSET" "$CODEX_SHA256" "${OS_MIRROR_BASE}/codex/${WANT_VERSION}/${CODEX_ASSET}" "$CODEX_URL"
  else
    curl -fsSL "$CODEX_URL" -o "$CODEX_TMP/$CODEX_ASSET"
  fi
  tar -xzf "$CODEX_TMP/$CODEX_ASSET" -C "$CODEX_TMP"
  install -m 0755 "$CODEX_TMP/${CODEX_ASSET%.tar.gz}" "$CODEX_BIN"
  rm -rf "$CODEX_TMP"
fi
"$CODEX_BIN" --version || {
  echo "[install-codex] ERROR: codex binary not runnable after install" >&2
  exit 1
}

mkdir -p /root/.codex/workspace /root/.codex/attachments

# config.toml + env + persona migration are owned ENTIRELY by the presync hook, NOT written here.
PRESYNC_HOOK="/usr/local/bin/runtime-codex-presync"
if [ -x "$PRESYNC_HOOK" ]; then
  echo "[install-codex] sync config/env now (via $PRESYNC_HOOK)"
  "$PRESYNC_HOOK" \
    || echo "[install-codex] WARN: presync failed (config.json missing? non-fatal — retried on next switch/boot)"
else
  echo "[install-codex] WARN: $PRESYNC_HOOK absent — os-server did not materialize it (standalone/offline run?); config/env NOT synced"
fi

# MUST stay in sync with runtimes/codex/gateway_unit.go codexUnitContent (EnsureOnboarding
# self-heals the same unit on a hand-switched device).
echo "[install-codex] write systemd unit codex.service"
cat >/etc/systemd/system/codex.service <<UNIT
[Unit]
Description=Codex agent gateway (os-server codex-gatewayd driving Codex App Server)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Environment=HOME=/root
EnvironmentFile=-/root/.codex/.env
WorkingDirectory=/root/.codex
ExecStart=/usr/local/bin/os-server codex-gatewayd
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
echo "[install-codex] enable + start codex.service"
systemctl enable --now codex.service
systemctl status codex.service --no-pager || true

echo "[install-codex] declare verify hook for switch-runtime (codex + os-server)"
mkdir -p /usr/local/lib/os-runtimes/codex
cat >/usr/local/lib/os-runtimes/codex/verify <<'VERIFY'
#!/usr/bin/env bash
command -v codex >/dev/null 2>&1 && [ -x /usr/local/bin/os-server ]
VERIFY
chmod +x /usr/local/lib/os-runtimes/codex/verify

echo "[install-codex] done — codex gatewayd installed + started (codex.service)."
echo "[install-codex] ===== install finished $(date -u '+%Y-%m-%dT%H:%M:%SZ') ====="
