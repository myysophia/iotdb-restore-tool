# IoTDB Restore Tool

[![Go Version](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat&logo=go)](https://golang.org/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Build](https://img.shields.io/badge/build-passing-brightgreen.svg)]()

> 用 Go 语言重构的 IoTDB 数据库恢复工具，支持从阿里云 OSS 自动下载备份并恢复到 Kubernetes 集群

---

## ✨ 特性

- 🚀 **自动化恢复** - 从 OSS 自动下载并导入 tsfile 文件到 IoTDB
- 🎯 **智能时间戳检测** - 自动检测备份文件时间戳（支持秒级范围 01-10）
- ⚡ **高性能并发** - 可配置的并发导入和批次处理
- 🔄 **断点续传** - 支持下载中断恢复
- 🔒 **文件锁机制** - 防止重复执行
- 📊 **结构化日志** - 使用 zap 实现详细日志记录
- 🐳 **Docker 支持** - 多阶段构建，最小化镜像体积
- ☸️ **Kubernetes 原生** - 支持 Job 和 CronJob 部署
- 💬 **企微通知** - 恢复完成后自动发送通知

---

## 📋 系统架构

```
┌─────────────┐      ┌──────────────┐      ┌─────────────┐      ┌─────────────┐
│   OSS 备份   │ ───> │  下载器模块   │ ───> │  Pod 传输   │ ───> │  IoTDB CLI  │
│  (阿里云)   │      │ (断点续传)    │      │ (kubectl)   │      │  (数据导入) │
└─────────────┘      └──────────────┘      └─────────────┘      └─────────────┘
                            │
                            ▼
                     ┌──────────────┐
                     │  批量导入器   │
                     │ (并发控制)    │
                     └──────────────┘
                            │
                            ▼
                     ┌──────────────┐
                     │  企微通知    │
                     │  (状态报告)   │
                     └──────────────┘
```

---

## 🛠️ 技术栈

- **语言**: Go 1.23+ (2500+ 行代码)
- **命令行框架**: [Cobra](https://github.com/spf13/cobra)
- **配置管理**: [Viper](https://github.com/spf13/viper)
- **Kubernetes**: [client-go](https://github.com/kubernetes/client-go)
- **日志**: [zap](https://github.com/uber-go/zap)
- **容器**: Docker 多阶段构建

---

## 🚀 快速开始

### 本地运行

```bash
# 构建
./build.sh

# 自动检测时间戳并恢复
./bin/iotdb-restore restore

# 指定时间戳
./bin/iotdb-restore restore -t 20260203083502
```

### Docker 运行

```bash
docker run --rm \
  -v ~/.kube/config:/root/.kube/config:ro \
  -v ./config.yaml:/etc/iotdb-restore/config.yaml:ro \
  iotdb-restore:latest
```

### Kubernetes CronJob

```bash
# 创建 RBAC 和 ConfigMap
kubectl apply -f deployments/k8s/

# 创建定时任务（每 2 小时执行一次）
kubectl apply -f deployments/k8s/cronjob.yaml
```

---

## 📖 使用场景

### 1. 定时自动恢复
```yaml
# CronJob: 每 2 小时自动恢复最新备份
schedule: "43 */2 * * *"
```

### 2. 手动恢复指定备份
```bash
./bin/iotdb-restore restore -t 20260319043502
```

### 3. 高并发批量导入
```bash
./bin/iotdb-restore restore --concurrency 3 --batch-size 50
```

### 4. 生产环境监控
```bash
# 检查 Pod 状态
./bin/iotdb-restore check
```

---

## 📊 性能优化

### 与 Bash 脚本对比

| 特性 | Bash 脚本 | Go 程序 | 提升 |
|------|----------|---------|------|
| Kubernetes 交互 | kubectl 命令 | client-go | ✅ 稳定可靠 |
| 并发控制 | xargs + flock | goroutine + channel | ⚡ 更高效 |
| 错误处理 | set -e | 结构化错误 + 重试 | 🛡️ 更健壮 |
| 配置管理 | 硬编码 | Viper 多源配置 | 📝 更灵活 |
| 部署方式 | 脚本文件 | 单一二进制 + Docker | 🐳 更便携 |

### 实测性能

- **文件处理**: 678 个 tsfile 文件
- **成功率**: 99.6% (675/678)
- **并发导入**: 支持自定义并发数
- **内存控制**: 批次暂停，防止 OOM
- **执行时间**: ~32 分钟（取决于文件大小）

---

## 📁 项目结构

```
iotdb-restore-tool/
├── cmd/iotdb-restore/     # 应用入口
├── pkg/
│   ├── config/            # 配置管理 (Viper)
│   ├── k8s/               # Kubernetes 集成 (client-go)
│   ├── downloader/        # OSS 下载器
│   ├── restorer/          # 恢复核心逻辑
│   ├── notifier/          # 企微通知
│   ├── logger/            # zap 日志
│   └── lock/              # 文件锁
├── configs/               # 配置文件
├── deployments/
│   ├── Dockerfile         # 多阶段构建
│   └── k8s/               # K8s 资源清单
└── docs/                  # 文档
```

---

## 🎯 核心功能

### 1. 自动备份检测
- 支持时间戳范围检测（秒级 01-10）
- 智能文件名匹配
- 自动下载最新备份

### 2. 本地下载+传输策略
```yaml
# 避免通过 Kubernetes NAT Gateway 产生流量费用
download_strategy: "local"
local_temp_dir: "/root/iotdb-restore"
```

### 3. 智能批次控制
```yaml
import:
  concurrency: 1          # 并发数
  batch_size: 3           # 批次大小
  batch_delay: 3          # 批次间延迟（秒）
  batch_pause: true       # 是否暂停
```

### 4. 企微通知
- Markdown 格式
- 详细的统计信息
- 成功/失败状态报告

---

## 🔧 配置示例

```yaml
kubernetes:
  namespace: iotdb
  pod_name: iotdb-datanode-0
  kubeconfig: /root/.kube/config-admin

iotdb:
  data_dir: /iotdb/data
  cli_path: /iotdb/sbin/start-cli.sh
  host: iotdb-datanode
  port: 6667

backup:
  base_url: https://iotdb-backup.oss-accelerate.aliyuncs.com/ems-au
  download_strategy: "local"      # 本地下载+传输
  local_temp_dir: "/root/iotdb-restore"
  auto_detect_timestamp: true

import:
  concurrency: 1
  batch_size: 3
  retry_count: 6
  batch_delay: 3
  batch_pause: true

notification:
  wechat:
    webhook_url: https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx
    enabled: true
  environment: EMS-AU
  enabled: true
```

---

## 📈 监控与日志

### 结构化日志
```json
{
  "level": "info",
  "msg": "恢复操作完成",
  "total_files": 678,
  "success_count": 675,
  "failed_count": 3,
  "duration": 1929.95
}
```

### 内存优化
- 批次暂停释放内存
- 可配置并发数
- 监控 Pod 状态

---

## 🔐 安全特性

- ✅ RBAC 权限控制（最小权限原则）
- ✅ 文件锁防止并发执行
- ✅ Kubeconfig 只读挂载
- ✅ 敏感信息环境变量化

---

## 📚 文档

- [README.md](README.md) - 详细使用指南
- [BUILD.md](BUILD.md) - 构建文档
- [docs/memory-tuning-20260319.md](docs/memory-tuning-20260319.md) - 内存优化实战案例

---

## 🤝 贡献指南

欢迎提交 Issue 和 Pull Request！

1. Fork 本仓库
2. 创建特性分支 (`git checkout -b feature/AmazingFeature`)
3. 提交更改 (`git commit -m 'Add some AmazingFeature'`)
4. 推送到分支 (`git push origin feature/AmazingFeature`)
5. 开启 Pull Request

---

## 📄 许可证

Copyright © 2025

---

## 🙏 致谢

- [Apache IoTDB](https://iotdb.apache.org/) - 时序数据库
- [Kubernetes](https://kubernetes.io/) - 容器编排平台
- [Cobra](https://github.com/spf13/cobra) - CLI 框架

---

## 📞 联系方式

- Issues: [GitHub Issues](https://github.com/your-org/iotdb-restore-tool/issues)
- Discussions: [GitHub Discussions](https://github.com/your-org/iotdb-restore-tool/discussions)

---

**⭐ 如果这个项目对您有帮助，请给个 Star！**
