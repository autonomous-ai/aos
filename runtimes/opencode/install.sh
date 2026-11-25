#!/usr/bin/env bash
# runtimes/opencode/install.sh — installer for the OpenCode agentic backend.
set -euo pipefail

# Tee all output to a log under /root/.opencode (persistent rootfs), NOT /var/log — on these boards
# /var/log is a volatile zram mount wiped on reboot.
OPENCODE_LOG="${OPENCODE_LOG:-/root/.opencode/install.log}"
mkdir -p "$(dirname "$OPENCODE_LOG")"
exec > >(tee -a "$OPENCODE_LOG") 2>&1
echo "[install-opencode] ===== install start $(date -u '+%Y-%m-%dT%H:%M:%SZ') (log: $OPENCODE_LOG) ====="

export HOME=/root
OPENCODE_BIN="/usr/local/bin/opencode"

# Latest published tag as of this build; see https://github.com/anomalyco/opencode/releases for
# newer.
OPENCODE_VERSION="${OPENCODE_VERSION:-1.18.4}"
# The CDN mirror holds the vendor's release archive; the official installer is the fallback.
OS_MIRROR_BASE="${OS_MIRROR_BASE:-https://cdn.autonomous.ai/os/mirror}"
# sha256 of the files this installer may take from the mirror ("<path> <sha256>"),
# the same values as MIRROR_PINS in software-update. A file without a pin comes
# from its vendor as before; a pinned file must match from the mirror or the vendor.
MIRROR_PINS='
opencode/1.18.4/opencode-linux-arm64.tar.gz eba87efba3976d533a24cca0316f8ef375b5f8e797c0a95c25ee919700b7ba35
opencode/1.18.4/opencode-linux-x64.tar.gz bab463c3fb3224d388bb7cfad63f38703df9cf0be2cfd2ce8cb49d886b53a174
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

echo "[install-opencode] prerequisites (jq, curl, tar)"
apt-get update || true
apt-get install -y jq curl tar || true

# The official script handles arch detection (linux arm64/x64), the .tar.gz asset name + extraction,
# and is idempotent (it exits early when the pinned version is already installed).
WANT_VERSION="${OPENCODE_VERSION#v}"
if [ -x "$OPENCODE_BIN" ] && "$OPENCODE_BIN" --version 2>/dev/null | grep -qw "$WANT_VERSION"; then
  echo "[install-opencode] opencode ${WANT_VERSION} already installed at $OPENCODE_BIN — skipping download"
else
  echo "[install-opencode] install opencode CLI ${WANT_VERSION} → /usr/local/bin"
  case "$(uname -m)" in
    aarch64|arm64) OC_ASSET="opencode-linux-arm64.tar.gz" ;;
    x86_64)        OC_ASSET="opencode-linux-x64.tar.gz" ;;
    *)             OC_ASSET="" ;;
  esac
  OC_SHA256=""
  [ -z "$OC_ASSET" ] || OC_SHA256="$(mirror_pin "opencode/${WANT_VERSION}/${OC_ASSET}")"
  OC_TMP="$(mktemp -d)"
  if [ -n "$OC_SHA256" ]; then
    # Pinned release archive from the mirror or the vendor; it must match before it is unpacked.
    fetch_pinned "$OC_TMP/$OC_ASSET" "$OC_SHA256" "${OS_MIRROR_BASE}/opencode/${WANT_VERSION}/${OC_ASSET}" \
      "https://github.com/anomalyco/opencode/releases/download/v${WANT_VERSION}/${OC_ASSET}"
    tar -xzf "$OC_TMP/$OC_ASSET" -C "$OC_TMP"
    install -m 0755 "$OC_TMP/opencode" "$OPENCODE_BIN"
    echo "[install-opencode] installed opencode ${WANT_VERSION} (pinned archive)"
  else
    curl -fsSL https://opencode.ai/install \
      | OPENCODE_INSTALL_DIR=/usr/local/bin bash -s -- --version "$WANT_VERSION"
  fi
  rm -rf "$OC_TMP"
fi
# Belt-and-suspenders: if the installer ignored OPENCODE_INSTALL_DIR (its behavior has drifted
# before), locate the binary it produced and copy it into /usr/local/bin so the systemd unit +
# verify hook find it at a stable path.
if [ ! -x "$OPENCODE_BIN" ]; then
  echo "[install-opencode] $OPENCODE_BIN missing — locating installer output"
  SRC="$(command -v opencode 2>/dev/null || true)"
  [ -x "$SRC" ] || SRC="/root/.opencode/bin/opencode"
  if [ -x "$SRC" ]; then
    install -m 0755 "$SRC" "$OPENCODE_BIN"
    echo "[install-opencode] copied $SRC → $OPENCODE_BIN"
  fi
fi
"$OPENCODE_BIN" --version || {
  echo "[install-opencode] ERROR: opencode binary not runnable after install" >&2
  exit 1
}

mkdir -p /root/.opencode/workspace /root/.opencode/attachments /root/.config/opencode

# opencode.json + env + persona migration are owned ENTIRELY by the presync hook, NOT written here.
PRESYNC_HOOK="/usr/local/bin/runtime-opencode-presync"
if [ -x "$PRESYNC_HOOK" ]; then
  echo "[install-opencode] sync config/env now (via $PRESYNC_HOOK)"
  "$PRESYNC_HOOK" \
    || echo "[install-opencode] WARN: presync failed (config.json missing? non-fatal — retried on next switch/boot)"
else
  echo "[install-opencode] WARN: $PRESYNC_HOOK absent — os-server did not materialize it (standalone/offline run?); config/env NOT synced"
fi

# MUST stay in sync with runtimes/opencode/gateway_unit.go opencodeUnitContent (EnsureOnboarding
# self-heals the same unit on a hand-switched device).
echo "[install-opencode] write systemd unit opencode.service"
cat >/etc/systemd/system/opencode.service <<UNIT
[Unit]
Description=OpenCode agent gateway (os-server opencode-gatewayd driving \`opencode run\` per turn)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Environment=HOME=/root
EnvironmentFile=-/root/.opencode/.env
WorkingDirectory=/root/.opencode
ExecStart=/usr/local/bin/os-server opencode-gatewayd
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
echo "[install-opencode] enable + start opencode.service"
systemctl enable --now opencode.service
systemctl status opencode.service --no-pager || true

echo "[install-opencode] declare verify hook for switch-runtime (opencode + os-server)"
mkdir -p /usr/local/lib/os-runtimes/opencode
cat >/usr/local/lib/os-runtimes/opencode/verify <<'VERIFY'
#!/usr/bin/env bash
command -v opencode >/dev/null 2>&1 && [ -x /usr/local/bin/os-server ]
VERIFY
chmod +x /usr/local/lib/os-runtimes/opencode/verify

echo "[install-opencode] done — opencode gatewayd installed + started (opencode.service)."
echo "[install-opencode] ===== install finished $(date -u '+%Y-%m-%dT%H:%M:%SZ') ====="
