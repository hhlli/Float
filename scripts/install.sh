#!/bin/bash
# Float Agent Multi-Platform Installation Script (Linux)

SERVER_URL=""
TOKEN=""
NODE_ID=""
REGISTER_TOKEN=""
GH_PROXY=""
SERVICE_NAME="float-agent"
BIN_NAME="float-agent"
PROBE_ARGS=""
ENABLE_DOCKER="false"
ENABLE_DOCKER_STATS="false"
DOWNLOAD_SOURCE="github"

echo "=========================================="
echo "🚀 Starting Float Agent Linux Probe Installation"
echo "=========================================="

# 1. Parse parameters
while [[ $# -gt 0 ]]; do
  case $1 in
    -s|--server) SERVER_URL="$2"; shift 2 ;;
    -t|--token) TOKEN="$2"; shift 2 ;;
    -id|--node-id) NODE_ID="$2"; shift 2 ;;
    --register) REGISTER_TOKEN="$2"; shift 2 ;;
    --gh-proxy) GH_PROXY="$2"; shift 2 ;;
    --service-name) SERVICE_NAME="$2"; shift 2 ;;
    --docker) ENABLE_DOCKER="true"; shift 1 ;;
    --docker-stats) ENABLE_DOCKER_STATS="true"; shift 1 ;;
    --insecure|--no-update|--include-buffer|--disable-rpc|--enable-terminal) PROBE_ARGS="$PROBE_ARGS $1"; shift 1 ;;
    --net-include|--net-exclude|--disk-mounts) PROBE_ARGS="$PROBE_ARGS $1 $2"; shift 2 ;;
    --source) DOWNLOAD_SOURCE="$2"; shift 2 ;;
    *) echo "❌ [ERROR] Unknown parameter: $1"; exit 1 ;;
  esac
done

if [ -z "$SERVER_URL" ]; then
  echo "❌ [Error] Missing required parameter: -s <Server>"
  exit 1
fi

if [ -z "$NODE_ID" ] && [ -z "$REGISTER_TOKEN" ]; then
  echo "❌ [Error] Missing identifier: -id <NodeID> or --register <Registration Token>"
  exit 1
fi
if [ -n "$NODE_ID" ] && [ -z "$TOKEN" ]; then
  echo "❌ [Error] When specifying static ID, -t <Token> must be provided"
  exit 1
fi

# 2. Privilege and path detection
SUDO_CMD=""
if [ "$(id -u)" -eq 0 ]; then
    INSTALL_DIR="/usr/local/bin"
    PRIV_LEVEL="Root"
elif command -v sudo >/dev/null 2>&1; then
    SUDO_CMD="sudo"
    INSTALL_DIR="/usr/local/bin"
    PRIV_LEVEL="Sudo"
else
    INSTALL_DIR="$HOME/.local/bin"
    PRIV_LEVEL="User ($INSTALL_DIR)"
fi

# 3. Architecture sniffing
ARCH=$(uname -m)
case $ARCH in
    x86_64|amd64)     BIN_ARCH="amd64" ;;
    aarch64|arm64)    BIN_ARCH="arm64" ;;
    armv7l|armv8l)    BIN_ARCH="arm" ;;
    i386|i686)        BIN_ARCH="386" ;;
    mips64*)          BIN_ARCH="mips64" ;;
    mips*)            BIN_ARCH="mips" ;;
    *) echo "❌ [Error] Unsupported architecture: $ARCH"; exit 1 ;;
esac

BIN_PATH="${INSTALL_DIR}/${BIN_NAME}"

if [ "$DOWNLOAD_SOURCE" == "server" ]; then
    PROBE_URL="${SERVER_URL}/float-agent-linux-${BIN_ARCH}"
else
    PROBE_URL="https://github.com/hhlli/Float-agent/releases/latest/download/float-agent-linux-${BIN_ARCH}"
fi

if [ -n "$GH_PROXY" ]; then
  [[ "${GH_PROXY}" != */ ]] && GH_PROXY="${GH_PROXY}/"
  PROBE_URL="${GH_PROXY}${PROBE_URL}"
fi

# Configuration Summary Output
echo ""
echo "[CONFIG] Installation configuration:"
echo "[CONFIG]   Service name: $SERVICE_NAME"
echo "[CONFIG]   Install directory: $INSTALL_DIR"
echo "[CONFIG]   Architecture: $ARCH -> linux-$BIN_ARCH"
echo "[CONFIG]   Privilege level: $PRIV_LEVEL"
echo "[CONFIG]   Docker integration: $ENABLE_DOCKER"
echo ""

# 4. Environment preparation and downloading
echo "📁 [1/4] Preparing installation directory and cleaning up old processes..."
$SUDO_CMD mkdir -p "$INSTALL_DIR"

$SUDO_CMD systemctl stop "$SERVICE_NAME" 2>/dev/null || true
$SUDO_CMD systemctl stop "${SERVICE_NAME}-docker-proxy" 2>/dev/null || true
pkill -9 "$SERVICE_NAME" 2>/dev/null || true

echo "⬇️ [2/4] Downloading binary file..."
echo "   -> Target path: $BIN_PATH"
echo "   -> Download source: $PROBE_URL"

# Removed silent flags (-s for curl, -q for wget) to display native progress bars
if command -v curl >/dev/null 2>&1; then
    $SUDO_CMD curl -L -o "$BIN_PATH" "$PROBE_URL"
elif command -v wget >/dev/null 2>&1; then
    $SUDO_CMD wget -O "$BIN_PATH" "$PROBE_URL"
else
    echo "❌ Neither curl nor wget found, cannot download."
    exit 1
fi

echo "🔑 [3/4] Granting executable permissions..."
$SUDO_CMD chmod +x "$BIN_PATH"

if [ "$ENABLE_DOCKER" == "true" ]; then
    PROBE_ARGS="$PROBE_ARGS -docker-endpoint http://127.0.0.1:23750"
    if [ "$ENABLE_DOCKER_STATS" == "true" ]; then
        PROBE_ARGS="$PROBE_ARGS -docker-stats"
    fi
fi

BASE_START_ARGS="-s \"$SERVER_URL\""
if [ -n "$TOKEN" ]; then BASE_START_ARGS="$BASE_START_ARGS -t \"$TOKEN\""; fi
if [ -n "$NODE_ID" ]; then BASE_START_ARGS="$BASE_START_ARGS -i \"$NODE_ID\""; fi
if [ -n "$REGISTER_TOKEN" ]; then BASE_START_ARGS="$BASE_START_ARGS -register \"$REGISTER_TOKEN\""; fi

START_CMD="$BIN_PATH $BASE_START_ARGS $PROBE_ARGS"
PROXY_START_CMD="$BIN_PATH docker-proxy -listen 127.0.0.1:23750"

# 5. Daemon registration and startup downgrade
echo "⚙️ [4/4] Registering and starting background service..."

if command -v systemctl >/dev/null 2>&1 && systemctl is-system-running >/dev/null 2>&1; then
    echo "   -> Detected init system: systemd"
    if [ "$ENABLE_DOCKER" == "true" ]; then
        PROXY_SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}-docker-proxy.service"
        $SUDO_CMD bash -c "cat > $PROXY_SERVICE_FILE" << EOF
[Unit]
Description=Float Agent Docker Proxy
After=docker.service
Requires=docker.service
[Service]
ExecStart=${PROXY_START_CMD}
Restart=always
RestartSec=10
[Install]
WantedBy=multi-user.target
EOF
        # Removed 2>/dev/null to show systemctl enable output (symlink creation)
        $SUDO_CMD systemctl enable "${SERVICE_NAME}-docker-proxy"
        $SUDO_CMD systemctl restart "${SERVICE_NAME}-docker-proxy"
        echo "   -> ✅ Docker proxy service started via Systemd."
    else
        # Removed 2>/dev/null to show systemctl disable output (symlink removal)
        $SUDO_CMD systemctl disable "${SERVICE_NAME}-docker-proxy" || true
        $SUDO_CMD rm -f "/etc/systemd/system/${SERVICE_NAME}-docker-proxy.service"
    fi

    SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
    $SUDO_CMD bash -c "cat > $SERVICE_FILE" << EOF
[Unit]
Description=Float Agent Monitor
After=network.target
[Service]
ExecStart=${START_CMD}
Restart=always
RestartSec=10
[Install]
WantedBy=multi-user.target
EOF
    $SUDO_CMD systemctl daemon-reload
    # Removed 2>/dev/null to show systemctl enable output
    $SUDO_CMD systemctl enable "$SERVICE_NAME"
    $SUDO_CMD systemctl restart "$SERVICE_NAME"
    echo "   -> ✅ Main probe service started via Systemd."

elif command -v rc-update >/dev/null 2>&1; then
    echo "   -> Detected init system: OpenRC"
    if [ "$ENABLE_DOCKER" == "true" ]; then
        PROXY_INIT_FILE="/etc/init.d/${SERVICE_NAME}-docker-proxy"
        $SUDO_CMD bash -c "cat > $PROXY_INIT_FILE" << EOF
#!/sbin/openrc-run
name="${SERVICE_NAME}-docker-proxy"
command="$BIN_PATH"
command_args="docker-proxy -listen 127.0.0.1:23750"
command_background="yes"
pidfile="/run/${SERVICE_NAME}-docker-proxy.pid"
EOF
        $SUDO_CMD chmod +x "$PROXY_INIT_FILE"
        # Removed 2>/dev/null to show rc-update output
        $SUDO_CMD rc-update add "${SERVICE_NAME}-docker-proxy" default
        $SUDO_CMD rc-service "${SERVICE_NAME}-docker-proxy" restart
        echo "   -> ✅ Docker proxy service started via OpenRC."
    else
        $SUDO_CMD rc-update del "${SERVICE_NAME}-docker-proxy" default || true
        $SUDO_CMD rm -f "/etc/init.d/${SERVICE_NAME}-docker-proxy"
    fi

    INIT_FILE="/etc/init.d/${SERVICE_NAME}"
    $SUDO_CMD bash -c "cat > $INIT_FILE" << EOF
#!/sbin/openrc-run
name="$SERVICE_NAME"
command="$BIN_PATH"
command_args="$BASE_START_ARGS $PROBE_ARGS"
command_background="yes"
pidfile="/run/${SERVICE_NAME}.pid"
EOF
    $SUDO_CMD chmod +x "$INIT_FILE"
    # Removed 2>/dev/null to show rc-update output
    $SUDO_CMD rc-update add "$SERVICE_NAME" default
    $SUDO_CMD rc-service "$SERVICE_NAME" restart
    echo "   -> ✅ Main probe service started via OpenRC."

else
    echo "   -> ⚠️ Systemd/OpenRC not detected, triggering downgraded startup plan."
    if command -v crontab >/dev/null 2>&1; then
        if [ "$ENABLE_DOCKER" == "true" ]; then
            (crontab -l 2>/dev/null | grep -v "$BIN_PATH"; echo "@reboot nohup $PROXY_START_CMD >/dev/null 2>&1 &"; echo "@reboot nohup $START_CMD >/dev/null 2>&1 &") | crontab -
        else
            (crontab -l 2>/dev/null | grep -v "$BIN_PATH"; echo "@reboot nohup $START_CMD >/dev/null 2>&1 &") | crontab -
        fi
        echo "   -> ✅ Written to crontab for auto-start on boot."
    else
        echo "   -> ⚠️ crontab unavailable, manual execution required after reboot."
    fi

    if [ "$ENABLE_DOCKER" == "true" ]; then
        eval "nohup $PROXY_START_CMD >/dev/null 2>&1 &"
    fi
    eval "nohup $START_CMD >/dev/null 2>&1 &"
    echo "   -> ✅ Process started in background via nohup."
fi

echo ""
echo "=========================================="
echo "✅ Installation and startup process completed!"
echo "=========================================="
echo "[STATUS] Service active: $SERVICE_NAME"
echo "[STATUS] Arguments: $PROBE_ARGS"