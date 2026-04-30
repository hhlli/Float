#!/bin/bash
# MacOS 探针安装脚本

SERVER_URL=""
TOKEN=""
NODE_ID=""
INSTALL_DIR="/usr/local/bin"
SERVICE_NAME="com.monitor.probe"
PROBE_ARGS=""

while [[ $# -gt 0 ]]; do
  case $1 in
    -s|--server) SERVER_URL="$2"; shift 2 ;;
    -t|--token) TOKEN="$2"; shift 2 ;;
    -id|--node-id) NODE_ID="$2"; shift 2 ;;
    --dir) INSTALL_DIR="$2"; shift 2 ;;
    --service-name) SERVICE_NAME="$2"; shift 2 ;;
    --insecure|--no-update|--include-buffer|--disable-rpc) PROBE_ARGS="$PROBE_ARGS $1"; shift 1 ;;
    --net-include|--net-exclude|--disk-mounts) PROBE_ARGS="$PROBE_ARGS $1 $2"; shift 2 ;;
    *) shift 1 ;;
  esac
done

if [ -z "$SERVER_URL" ] || [ -z "$TOKEN" ]; then
  echo "❌ 缺少必要参数。"
  exit 1
fi

ARCH=$(uname -m)
BIN_ARCH="amd64"
if [[ "$ARCH" == "arm64" ]]; then BIN_ARCH="arm64"; fi

PROBE_URL="${SERVER_URL}/probe-darwin-${BIN_ARCH}"
BIN_PATH="${INSTALL_DIR}/${SERVICE_NAME}"

echo "⬇️ 正在下载 Mac 探针..."
sudo mkdir -p "$INSTALL_DIR"
sudo curl -sL -o "$BIN_PATH" "$PROBE_URL"
sudo chmod +x "$BIN_PATH"

# 🌟 核心修复：动态构建 Launchd 的 XML 参数数组
ARGS_XML="<string>${BIN_PATH}</string>
        <string>-s</string><string>${SERVER_URL}</string>
        <string>-t</string><string>${TOKEN}</string>"

# 注入节点 ID
if [ -n "$NODE_ID" ]; then
    ARGS_XML="${ARGS_XML}
        <string>-i</string><string>${NODE_ID}</string>"
fi

# 注入所有高级参数
if [ -n "$PROBE_ARGS" ]; then
    for arg in $PROBE_ARGS; do
        ARGS_XML="${ARGS_XML}
        <string>${arg}</string>"
    done
fi

# 创建 launchd plist
PLIST_PATH="/Library/LaunchDaemons/${SERVICE_NAME}.plist"
echo "⚙️ 正在配置后台服务..."
sudo bash -c "cat > $PLIST_PATH" << EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${SERVICE_NAME}</string>
    <key>ProgramArguments</key>
    <array>
        ${ARGS_XML}
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
EOF

sudo launchctl unload "$PLIST_PATH" 2>/dev/null
sudo launchctl load -w "$PLIST_PATH"
echo "✅ MacOS 探针安装并启动完成！"