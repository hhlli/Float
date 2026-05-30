#!/bin/bash
# MacOS Float Agent 探针安装脚本 (对齐 Linux 权限嗅探与降级逻辑)

SERVER_URL=""
TOKEN=""
NODE_ID=""
REGISTER_TOKEN=""
CUSTOM_INSTALL_DIR=""
BIN_NAME="float-agent"
SERVICE_NAME="com.float.agent"
PROBE_ARGS=""

echo "=========================================="
echo "🚀 开始安装 Float Agent MacOS 探针"
echo "=========================================="

while [[ $# -gt 0 ]]; do
  case $1 in
    -s|--server) SERVER_URL="$2"; shift 2 ;;
    -t|--token) TOKEN="$2"; shift 2 ;;
    -id|--node-id) NODE_ID="$2"; shift 2 ;;
    --register) REGISTER_TOKEN="$2"; shift 2 ;;
    --dir) CUSTOM_INSTALL_DIR="$2"; shift 2 ;;
    --service-name) SERVICE_NAME="$2"; shift 2 ;;
    --insecure|--no-update|--include-buffer|--disable-rpc|--enable-terminal) PROBE_ARGS="$PROBE_ARGS $1"; shift 1 ;;
    --net-include|--net-exclude|--disk-mounts) PROBE_ARGS="$PROBE_ARGS $1 $2"; shift 2 ;;
    *) shift 1 ;;
  esac
done

if [ -z "$SERVER_URL" ]; then
  echo "❌ [错误] 缺少必要参数: 必须提供 -s <Server>"
  exit 1
fi
if [ -z "$NODE_ID" ] && [ -z "$REGISTER_TOKEN" ]; then
  echo "❌ [错误] 缺少标识: 必须提供 -id <NodeID> 或 --register <注册密钥>"
  exit 1
fi
if [ -n "$NODE_ID" ] && [ -z "$TOKEN" ]; then
  echo "❌ [错误] 静态指定 ID 时，必须提供 -t <Token>"
  exit 1
fi
echo "✅ [1/6] 参数校验通过"

SUDO_CMD=""
if [ "$(id -u)" -eq 0 ]; then
    INSTALL_DIR="${CUSTOM_INSTALL_DIR:-/usr/local/bin}"
    PLIST_DIR="/Library/LaunchDaemons"
    echo "✅ [2/6] 检测到 Root 权限"
elif command -v sudo >/dev/null 2>&1; then
    SUDO_CMD="sudo"
    INSTALL_DIR="${CUSTOM_INSTALL_DIR:-/usr/local/bin}"
    PLIST_DIR="/Library/LaunchDaemons"
    echo "✅ [2/6] 检测到 Sudo 权限"
else
    INSTALL_DIR="${CUSTOM_INSTALL_DIR:-$HOME/.local/bin}"
    PLIST_DIR="$HOME/Library/LaunchAgents"
    echo "⚠️ [2/6] 无 Root/Sudo 权限，降级安装至用户目录: $INSTALL_DIR"
fi

ARCH=$(uname -m)
BIN_ARCH="amd64"
if [[ "$ARCH" == "arm64" ]]; then BIN_ARCH="arm64"; fi
echo "🔍 [3/6] 检测到系统架构: $ARCH (对应探针版本: darwin-$BIN_ARCH)"

PROBE_URL="${SERVER_URL}/float-agent-darwin-${BIN_ARCH}"
BIN_PATH="${INSTALL_DIR}/${BIN_NAME}"

echo "📁 [4/6] 准备安装目录并下载二进制文件..."
echo "   -> 目标路径: $BIN_PATH"
$SUDO_CMD mkdir -p "$INSTALL_DIR"
$SUDO_CMD curl -sL -o "$BIN_PATH" "$PROBE_URL"
$SUDO_CMD chmod +x "$BIN_PATH"

ARGS_XML="<string>${BIN_PATH}</string>
        <string>-s</string><string>${SERVER_URL}</string>"

if [ -n "$TOKEN" ]; then
    ARGS_XML="${ARGS_XML}
        <string>-t</string><string>${TOKEN}</string>"
fi
if [ -n "$NODE_ID" ]; then
    ARGS_XML="${ARGS_XML}
        <string>-i</string><string>${NODE_ID}</string>"
fi
if [ -n "$REGISTER_TOKEN" ]; then
    ARGS_XML="${ARGS_XML}
        <string>-register</string><string>${REGISTER_TOKEN}</string>"
fi
if [ -n "$PROBE_ARGS" ]; then
    for arg in $PROBE_ARGS; do
        ARGS_XML="${ARGS_XML}
        <string>${arg}</string>"
    done
fi

PLIST_PATH="${PLIST_DIR}/${SERVICE_NAME}.plist"
echo "⚙️ [5/6] 生成守护进程配置..."
echo "   -> 配置文件: $PLIST_PATH"
$SUDO_CMD mkdir -p "$PLIST_DIR"
$SUDO_CMD bash -c "cat > \"$PLIST_PATH\"" << EOF
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

echo "🔄 [6/6] 注册并启动服务..."
$SUDO_CMD launchctl unload "$PLIST_PATH" 2>/dev/null
$SUDO_CMD launchctl load -w "$PLIST_PATH"

echo "=========================================="
echo "✅ 安装与启动完成！"
echo "=========================================="