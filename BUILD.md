# 构建指南

## 快速构建

### 使用构建脚本（推荐）

```bash
# 使用默认版本（1.2.0）
./build.sh

# 指定版本号
VERSION=1.3.0 ./build.sh
```

### 手动构建

```bash
# 设置版本信息
VERSION=1.2.0
COMMIT=$(git rev-parse --short HEAD)
BUILD_DATE=$(date -u '+%Y-%m-%d_%H:%M:%S')

# 构建
go build -ldflags="-X 'main.Version=$VERSION' -X 'main.Commit=$COMMIT' -X 'main.Date=$BUILD_DATE'" \
  -o bin/iotdb-restore ./cmd/iotdb-restore
```

## 交叉编译

### Linux (amd64)
```bash
GOOS=linux GOARCH=amd64 go build -ldflags="..." -o bin/iotdb-restore-linux-amd64 ./cmd/iotdb-restore
```

### macOS (amd64)
```bash
GOOS=darwin GOARCH=amd64 go build -ldflags="..." -o bin/iotdb-restore-darwin-amd64 ./cmd/iotdb-restore
```

### macOS (ARM64/Apple Silicon)
```bash
GOOS=darwin GOARCH=arm64 go build -ldflags="..." -o bin/iotdb-restore-darwin-arm64 ./cmd/iotdb-restore
```

## 验证版本

```bash
./bin/iotdb-restore version
# 输出: IoTDB Restore Tool v1.2.0 (commit: 04ad796, built at: 2026-02-26_10:04:22)
```

## 版本信息

版本信息在编译时通过 `-ldflags` 注入：

| 变量 | 说明 | 示例 |
|------|------|------|
| `Version` | 版本号 | 1.2.0 |
| `Commit` | Git commit hash | 04ad796 |
| `Date` | 构建时间 | 2026-02-26_10:04:22 |

## 生产构建建议

### 1. 启用优化
```bash
go build -ldflags="-s -w ..."  # 减小二进制文件大小
```

### 2. 使用 UPX 压缩（可选）
```bash
upx --best --lzma bin/iotdb-restore
```

### 3. 交叉编译多平台
```bash
# 构建所有平台
./scripts/build-all.sh
```

## Docker 构建

```dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -ldflags="..." -o iotdb-restore ./cmd/iotdb-restore

FROM alpine:latest
COPY --from=builder /app/iotdb-restore /usr/local/bin/
ENTRYPOINT ["iotdb-restore"]
```
