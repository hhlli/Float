#!/bin/bash
# Monitor Probe 全平台兼容安装脚本 (支持无 root、非 Systemd 系统)

SERVER_URL=""
TOKEN=""
NODE_ID=""
GH_PROXY=""
SERVICE_NAME="go-probe"
PROBE_ARGS=""

# 1. 解析参数
while [[ $# -gt 0 ]]; do
  case $1 in
    -s|--server) SERVER_URL="$2"; shift 2 ;;
    -t|--token) TOKEN="$2"; shift 2 ;;
    -id|--node-id) NODE_ID="$2"; shift 2 ;;
    --gh-proxy) GH_PROXY="$2"; shift 2 ;;
    --service-name) SERVICE_NAME="$2"; shift 2 ;;
    --insecure|--no-update|--include-buffer|--disable-rpc) PROBE_ARGS="$PROBE_ARGS $1"; shift 1 ;;
    --net-include|--net-exclude|--disk-mounts) PROBE_ARGS="$PROBE_ARGS $1 $2"; shift 2 ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
done

if [ -z "$SERVER_URL" ] || [ -z "$TOKEN" ]; then
  echo "❌ 缺少必要参数: -s <Server> -t <Token>"
  exit 1
fi

if [ -z "$NODE_ID" ]; then
  NODE_ID="$(hostname | sed 's/[^a-zA-Z0-9-]/_/g')-$(cat /dev/urandom | tr -dc 'a-z0-9' | fold -w 4 | head -n 1)"
  echo "ℹ️ 已自动生成节点 ID: $NODE_ID"
fi

# 2. 权限与路径探测 (核心改造 1)
SUDO_CMD=""
if [ "$(id -u)" -eq 0 ]; then
    INSTALL_DIR="/usr/local/bin"
elif command -v sudo >/dev/null 2>&1; then
    SUDO_CMD="sudo"
    INSTALL_DIR="/usr/local/bin"
else
    # 无 Root 且无 Sudo 时的降级路径 (如 Termux 或普通用户)
    INSTALL_DIR="$HOME/.local/bin"
    echo "⚠️ 无 Root 权限，降级安装至用户目录: $INSTALL_DIR"
fi

# 3. 更广泛的架构嗅探 (核心改造 2)
ARCH=$(uname -m)
case $ARCH in
    x86_64|amd64)     BIN_ARCH="amd64" ;;
    aarch64|arm64)    BIN_ARCH="arm64" ;;
    armv7l|armv8l)    BIN_ARCH="arm" ;;
    i386|i686)        BIN_ARCH="386" ;;
    mips64*)          BIN_ARCH="mips64" ;;
    mips*)            BIN_ARCH="mips" ;;
    *) echo "❌ 暂不支持的架构: $ARCH"; exit 1 ;;
esac

BIN_PATH="${INSTALL_DIR}/${SERVICE_NAME}"
PROBE_URL="${SERVER_URL}/probe-linux-${BIN_ARCH}"
if [ -n "$GH_PROXY" ]; then
  [[ "${GH_PROXY}" != */ ]] && GH_PROXY="${GH_PROXY}/"
  PROBE_URL="${GH_PROXY}${PROBE_URL}"
fi

# 4. 环境准备与下载
$SUDO_CMD mkdir -p "$INSTALL_DIR"

# 强杀旧进程
$SUDO_CMD systemctl stop "$SERVICE_NAME" 2>/dev/null
pkill -9 "$SERVICE_NAME" 2>/dev/null

echo "⬇️ 下载探针: $PROBE_URL"
if command -v curl >/dev/null 2>&1; then
    $SUDO_CMD curl -sL -o "$BIN_PATH" "$PROBE_URL"
elif command -v wget >/dev/null 2>&1; then
    $SUDO_CMD wget -q -O "$BIN_PATH" "$PROBE_URL"
else
    echo "❌ 找不到 curl 或 wget，无法下载。"
    exit 1
fi

$SUDO_CMD chmod +x "$BIN_PATH"

# 构建完整启动命令
START_CMD="$BIN_PATH -s \"$SERVER_URL\" -t \"$TOKEN\" -i \"$NODE_ID\" $PROBE_ARGS"

# 5. 守护进程注册与启动降级 (核心改造 3)
echo "⚙️ 正在注册并启动服务..."

if command -v systemctl >/dev/null 2>&1 && systemctl is-system-running >/dev/null 2>&1; then
    # 方案 A: 标准 Systemd (绝大多数 Linux)
    SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
    $SUDO_CMD bash -c "cat > $SERVICE_FILE" << EOF
[Unit]
Description=Monitor Agent
After=network.target
[Service]
ExecStart=${START_CMD}
Restart=always
RestartSec=10
[Install]
WantedBy=multi-user.target
EOF
    $SUDO_CMD systemctl daemon-reload
    $SUDO_CMD systemctl enable "$SERVICE_NAME"
    $SUDO_CMD systemctl restart "$SERVICE_NAME"
    echo "✅ 已通过 Systemd 启动。"

elif command -v rc-update >/dev/null 2>&1; then
    # 方案 B: OpenRC (Alpine Linux / 容器环境)
    INIT_FILE="/etc/init.d/${SERVICE_NAME}"
    $SUDO_CMD bash -c "cat > $INIT_FILE" << EOF
#!/sbin/openrc-run
name="$SERVICE_NAME"
command="$BIN_PATH"
command_args="-s $SERVER_URL -t $TOKEN -i $NODE_ID $PROBE_ARGS"
command_background="yes"
pidfile="/run/${SERVICE_NAME}.pid"
EOF
    $SUDO_CMD chmod +x "$INIT_FILE"
    $SUDO_CMD rc-update add "$SERVICE_NAME" default
    $SUDO_CMD rc-service "$SERVICE_NAME" restart
    echo "✅ 已通过 OpenRC 启动。"

else
    # 方案 C: 无守护进程环境 (Termux / 阉割版 Docker / 共享主机)
    echo "⚠️ 未检测到受支持的 init 系统 (如 Systemd/OpenRC)。"
    
    # 尝试使用 crontab 实现开机自启
    if command -v crontab >/dev/null 2>&1; then
        (crontab -l 2>/dev/null | grep -v "$BIN_PATH"; echo "@reboot nohup $START_CMD >/dev/null 2>&1 &") | crontab -
        echo "✅ 已通过 crontab @reboot 写入开机自启。"
    else
        echo "⚠️ 警告: crontab 不可用，探针无法开机自启，重启后需手动执行。"
    fi

    # 直接使用 nohup 后台运行
    eval "nohup $START_CMD >/dev/null 2>&1 &"
    echo "✅ 已通过 nohup 在后台启动进程。"
fi

echo "========================================="
echo "🎉 部署脚本执行完成！"