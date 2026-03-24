#!/bin/bash
# 构建带版本的 IoTDB Restore Tool 二进制文件

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 默认版本信息
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo "dev")}
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE=$(date -u '+%Y-%m-%d_%H:%M:%S')

# 输出目录
OUTPUT_DIR=${OUTPUT_DIR:-"bin"}
BINARY_NAME=${BINARY_NAME:-"iotdb-restore"}

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}  IoTDB Restore Tool 构建脚本${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo "版本信息:"
echo "  Version:   ${VERSION}"
echo "  Commit:    ${COMMIT}"
echo "  Build:     ${BUILD_DATE}"
echo ""

# 创建输出目录
mkdir -p "$OUTPUT_DIR"

echo -e "${YELLOW}开始构建...${NC}"

# 构建带版本的二进制文件
go build \
  -ldflags="-X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.Date=${BUILD_DATE}'" \
  -o "${OUTPUT_DIR}/${BINARY_NAME}" \
  ./cmd/iotdb-restore

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✅ 构建成功！${NC}"
    echo ""
    echo "二进制文件信息:"
    echo "  路径:   $(pwd)/${OUTPUT_DIR}/${BINARY_NAME}"
    echo "  大小:   $(du -h ${OUTPUT_DIR}/${BINARY_NAME} | cut -f1)"
    echo ""
    echo "验证版本:"
    ./${OUTPUT_DIR}/${BINARY_NAME} version
    echo ""
    echo -e "${GREEN}========================================${NC}"
else
    echo -e "${RED}❌ 构建失败！${NC}"
    exit 1
fi
