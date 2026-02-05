# 构建阶段
FROM golang:1.23-alpine AS builder

# 安装构建依赖
RUN apk add --no-cache git make

# 设置工作目录
WORKDIR /build

# 复制 go mod 文件
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码
COPY . .

# 构建应用（静态链接）
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -ldflags="-w -s" -o iotdb-restore ./cmd/iotdb-restore

# 运行阶段
FROM alpine:3.19

# 安装运行时依赖
RUN apk add --no-cache ca-certificates tzdata curl

# 设置时区
ENV TZ=Asia/Shanghai

# 创建非 root 用户
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

# 复制二进制文件
COPY --from=builder /build/iotdb-restore /usr/local/bin/

# 复制默认配置
COPY configs/config.yaml /etc/iotdb-restore/config.yaml

# 创建日志目录
RUN mkdir -p /var/log && chown -R appuser:appuser /var/log

# 切换用户
USER appuser

# 入口点
ENTRYPOINT ["iotdb-restore"]

# 默认命令
CMD ["--help"]
