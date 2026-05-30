# Float

简体中文 | English

Float 是一个分布式的服务器状态观测与管理平台。本项目由人类开发者进行主导与需求定义，核心逻辑及代码实现由大语言模型（Gemini、Claude、ChatGPT）协同编写完成。后端采用 Go 语言，前端采用 Vue3 框架。系统通过代理端点采集系统指标，并在控制节点通过 Web 终端进行可视化呈现。


## 部署说明

### 方式一：Docker Compose 部署

初始化工作空间并创建 `docker-compose.yml` 配置文件：

```yaml
services:
  float-server:
    image: ghcr.io/hhlli/float:latest
    container_name: float-server
    restart: always
    environment:
      - TZ=Asia/Shanghai
    ports:
      - "21213:8080"
    volumes:
      - ./data:/app/data
      - ./data/logs:/app/logs

```

执行镜像拉取与后台启动流程：

```bash
docker compose pull
docker compose up -d

```

控制面板访问入口：`http://<服务器IP>:21213`

### 方式二：Linux 二进制部署 (AMD64)

构建运行时基础环境目录：

```bash
mkdir -p /opt/float/{data,logs}
cd /opt/float

```

下载 Release 产物并配置可执行权限：

```bash
wget https://github.com/hhlli/Float/releases/latest/download/float-server-linux-amd64
chmod +x float-server-linux-amd64

```

*(可选配置)* 写入 Systemd 守护进程配置至 `/etc/systemd/system/float-server.service`：

```ini
[Unit]
Description=Float Server Daemon
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/float
ExecStart=/opt/float/float-server-linux-amd64 serve
Restart=always
RestartSec=5
LimitNOFILE=65535
Environment="TZ=Asia/Shanghai"

[Install]
WantedBy=multi-user.target

```

重载进程栈并设置开机自启：

```bash
systemctl daemon-reload
systemctl enable --now float-server

```

## 源代码编译

**基础环境限制**

* Go 1.21+
* Node.js 20+

**步骤 1：前端资源打包**

```bash
git clone https://github.com/hhlli/Float-web.git
cd Float-web
npm install
npm run build

```

**步骤 2：服务端编译及资源整合**

```bash
git clone https://github.com/hhlli/Float.git
cd Float

```

提取前端构建输出的 `dist` 目录，将其完整放置于后端主项目路径下。

执行服务端静态编译指令：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o Float .

```

挂载命令参数启动进程：

```bash
./Float serve

```
