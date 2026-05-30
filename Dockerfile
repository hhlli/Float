FROM alpine:latest

# 安装时区和根证书依赖
RUN apk add --no-cache tzdata ca-certificates libc6-compat

WORKDIR /app

# 拷贝本地已编译的二进制文件及前端静态资源
# 假设编译后的后端二进制文件名为 Float
COPY Float /app/Float
COPY dist /app/dist

# 拷贝部署脚本
# 【核心修改】完整保留 scripts 目录结构，直接拷贝整个文件夹
COPY scripts /app/scripts

# 拷贝所有探针二进制文件
COPY float-agent-* /app/

# 赋予执行权限
RUN chmod +x /app/Float

EXPOSE 8080

CMD ["/app/Float", "serve"]