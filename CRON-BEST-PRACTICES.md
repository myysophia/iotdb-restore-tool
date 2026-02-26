# Cron 并发问题与解决方案

## 🔴 问题场景

### 当前情况
- **恢复任务耗时**: 约 1 小时
- **Cron 频率**: 每小时执行一次
- **结果**: 多个任务同时运行 → 资源冲突、数据损坏

### 问题演示

```bash
00:00 - CronJob #1 开始（预计 01:00 完成）
         ↓
00:30 - #1 正在解压、导入中...
         ↓
01:00 - CronJob #2 开始执行 ← 问题！#1 还没完成
         ↓
01:30 - #1 和 #2 同时在导入 tsfile → 数据库冲突
         ↓
02:00 - CronJob #3 开始执行 ← 3 个任务同时运行！
         ↓
02:15 - Pod 崩溃（OOM 或 CPU 过载）
```

---

## ✅ 解决方案

### 方案 1: 调整执行间隔（推荐）⭐

#### 原则
```
执行间隔 = 任务耗时 + 安全缓冲
```

如果任务需要 1 小时，建议执行间隔：
- **最小**: 1.5 小时（30 分钟缓冲）
- **推荐**: 2 小时（1 小时缓冲）
- **理想**: 3 小时（2 小时缓冲）

#### 推荐的 Cron 表达式

| Cron 表达式 | 执行频率 | 说明 |
|------------|---------|------|
| `0 */2 * * *` | 每 2 小时 | ✅ 推荐（留有缓冲） |
| `0 */3 * * *` | 每 3 小时 | ✅ 理想（充足缓冲） |
| `0 */4 * * *` | 每 4 小时 | ✅ 安全（间隔较大） |
| `0 2,6,10,14,18,22 * * *` | 每天 6 次（每 4 小时） | ✅ 业务低峰期 |
| `0 */6 * * *` | 每 6 小时 | ✅ 最安全（适用于慢速恢复） |

#### ❌ 不推荐的配置

| Cron 表达式 | 问题 | 风险 |
|------------|------|------|
| `0 * * * *` | 每小时执行 | 🔴 高风险（任务重叠） |
| `*/30 * * * *` | 每 30 分钟 | 🔴🔴 严重风险（严重重叠） |
| `0 */1 * * *` | 每小时执行 | 🔴 高风险（无缓冲） |

---

### 方案 2: Kubernetes CronJob 并发控制

在 `deployments/k8s/cronjob.yaml` 中配置：

```yaml
spec:
  schedule: "0 */2 * * *"           # 每 2 小时执行一次
  concurrencyPolicy: Forbid         # 如果上次任务未完成，跳过本次
  activeDeadlineSeconds: 7200       # 2 小时超时（防止任务卡死）
  successfulJobsHistoryLimit: 3     # 保留 3 次成功记录
  failedJobsHistoryLimit: 1         # 保留 1 次失败记录
```

#### 并发策略对比

| 策略 | 说明 | 适用场景 |
|------|------|----------|
| **Allow**（默认） | 允许并发执行 | ❌ 不适用于长时间任务 |
| **Forbid** | 禁止并发，跳过执行 | ✅ 推荐（防止重叠） |
| **Replace** | 终止旧任务，执行新任务 | ⚠️ 谨慎使用（可能导致数据不一致） |

**当前配置**: `Forbid` ✅

---

### 方案 3: 文件锁机制（已实现）

#### 工作原理

```
启动时 → 尝试获取锁
         ↓
    锁文件存在？→ 是 → 检查锁文件年龄
         ↓                    ↓
        否                   ↓
    获取锁成功           超过 3 小时？
                              ↓
                    是 → 删除旧锁，重试
                    否 → 退出，提示错误
```

#### 文件锁特性

- ✅ **自动检测**: 启动时自动检测是否已有任务运行
- ✅ **死锁处理**: 超过 3 小时的锁文件自动清理
- ✅ **友好提示**: 告知用户如何手动清理锁文件
- ✅ **自动释放**: 程序退出时自动释放锁

#### 锁文件位置

```bash
/tmp/iotdb-restore.lock
```

#### 锁文件内容

```
12345                    # 进程 PID
started_at: 2026-02-05T06:00:00+08:00  # 启动时间
```

---

## 📊 完整的多层防护

```
┌─────────────────────────────────────────┐
│  第 1 层: Kubernetes CronJob            │
│  ┌───────────────────────────────────┐  │
│  │ concurrencyPolicy: Forbid        │  │ ← 如果任务运行中，跳过本次
│  └───────────────────────────────────┘  │
└─────────────────────────────────────────┘
                  ↓
┌─────────────────────────────────────────┐
│  第 2 层: 文件锁                        │
│  ┌───────────────────────────────────┐  │
│  │ /tmp/iotdb-restore.lock          │  │ ← 如果锁存在，直接退出
│  └───────────────────────────────────┘  │
└─────────────────────────────────────────┘
                  ↓
┌─────────────────────────────────────────┐
│  第 3 层: 超时保护                      │
│  ┌───────────────────────────────────┐  │
│  │ activeDeadlineSeconds: 7200      │  │ ← 2 小时后强制终止
│  └───────────────────────────────────┘  │
└─────────────────────────────────────────┘
```

---

## 🚀 使用指南

### 场景 1: Kubernetes CronJob（推荐）

```bash
# 1. 更新 CronJob 配置
kubectl apply -f deployments/k8s/cronjob.yaml

# 2. 查看 CronJob 状态
kubectl get cronjob -n iotdb

# 3. 查看执行历史
kubectl get jobs -n iotdb --sort-by=.metadata.creationTimestamp

# 4. 查看最近的任务日志
kubectl logs -f job/iotdb-restore-<timestamp> -n iotdb

# 5. 手动触发一次测试
kubectl create job iotdb-restore-test --from=cronjob/iotdb-restore -n iotdb
```

### 场景 2: 系统 Crontab

```bash
# 编辑 crontab
crontab -e

# 添加任务（每 2 小时执行）
0 */2 * * * /opt/vnnox-configcenter-data/iotdb-restore-tool/bin/iotdb-restore restore >> /var/log/iotdb-restore.log 2>&1
```

**注意**: 文件锁会自动防止并发，但仍建议设置合理的执行间隔。

---

## 📝 日志输出

### 正常启动（获取锁成功）

```bash
2026-02-05T06:00:00.123Z	INFO	main.go:135	IoTDB Restore Tool 启动	{...}
2026-02-05T06:00:00.234Z	INFO	main.go:164	文件锁获取成功	{"lock_file": "/tmp/iotdb-restore.lock"}
2026-02-05T06:00:00.345Z	INFO	main.go:189	自动检测到时间戳	{...}
```

### 锁已被占用（自动退出）

```bash
2026-02-05T07:00:00.123Z	INFO	main.go:135	IoTDB Restore Tool 启动	{...}
2026-02-05T07:00:00.234Z	ERROR	main.go:153	无法获取锁，可能已有任务在运行	{
  "error": "任务正在运行中（PID: 12345，锁文件: /tmp/iotdb-restore.lock）",
  "lock_file": "/tmp/iotdb-restore.lock"
}

⚠️  错误: 任务正在运行中（PID: 12345，锁文件: /tmp/iotdb-restore.lock）

如果确定没有其他任务在运行，可以手动删除锁文件:
  rm -f /tmp/iotdb-restore.lock
```

### 死锁自动清理（锁文件超过 3 小时）

```bash
# 自动检测并清理死锁
2026-02-05T09:00:00.123Z	INFO	main.go:135	IoTDB Restore Tool 启动	{...}
2026-02-05T09:00:00.234Z	INFO	main.go:164	文件锁获取成功	{
  "lock_file": "/tmp/iotdb-restore.lock",
  "note": "死锁已自动清理（原锁文件创建于 06:00，已超过 3 小时）"
}
```

---

## 🔧 故障排查

### 问题 1: 任务总是被跳过

**症状**: CronJob 触发但没有新任务执行

**原因**:
1. 上次任务还在运行（被 `concurrencyPolicy: Forbid` 拦截）
2. 锁文件未被清理

**解决**:
```bash
# 检查是否有任务在运行
kubectl get jobs -n iotdb

# 检查锁文件
ls -la /tmp/iotdb-restore.lock

# 如果确认没有任务在运行，手动删除锁
rm -f /tmp/iotdb-restore.lock
```

---

### 问题 2: 任务频繁超时

**症状**: 任务总是达到 `activeDeadlineSeconds` 被终止

**原因**:
1. 恢复数据量太大
2. 并发数太低
3. 网络/磁盘 I/O 太慢

**解决**:
```yaml
# 方案 1: 增加超时时间
activeDeadlineSeconds: 14400  # 4 小时

# 方案 2: 增加导入并发
import:
  concurrency: 4  # 从 1 改为 4
  batch_size: 50  # 从 3 改为 50

# 方案 3: 延长执行间隔
schedule: "0 */4 * * *"  # 每 4 小时执行一次
```

---

### 问题 3: CronJob 历史记录太多

**症状**: `kubectl get jobs` 返回太多记录

**解决**:
```yaml
# 在 cronjob.yaml 中调整历史记录限制
successfulJobsHistoryLimit: 3  # 只保留 3 次成功记录
failedJobsHistoryLimit: 1      # 只保留 1 次失败记录
```

---

## ✅ 最佳实践总结

### Cron 配置

| 任务耗时 | 推荐频率 | Cron 表达式 | 缓冲时间 |
|---------|---------|-------------|----------|
| 30 分钟 | 每 1 小时 | `0 * * * *` | 30 分钟 |
| 1 小时 | 每 2 小时 | `0 */2 * * *` | 1 小时 |
| 1.5 小时 | 每 3 小时 | `0 */3 * * *` | 1.5 小时 |
| 2 小时 | 每 4 小时 | `0 */4 * * *` | 2 小时 |

### 必须配置

```yaml
concurrencyPolicy: Forbid        # 必须！禁止并发
activeDeadlineSeconds: 10800     # 必须！3 小时超时
successfulJobsHistoryLimit: 3    # 推荐！清理历史
failedJobsHistoryLimit: 1        # 推荐！清理历史
```

### 监控建议

1. **定期检查 Job 状态**
   ```bash
   kubectl get jobs -n iotdb --sort-by=.metadata.creationTimestamp | tail -10
   ```

2. **检查任务执行时长**
   ```bash
   kubectl get jobs -n iotdb -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.startTime}{"\t"}{.status.completionTime}{"\n"}{end}'
   ```

3. **设置告警**
   - 任务失败时发送企微通知（已实现）
   - 任务超时时发送告警
   - 多次跳过执行时发送警告

---

## 📋 配置检查清单

部署前请确认：

- [ ] ✅ `schedule` 执行间隔 > 任务耗时 + 缓冲
- [ ] ✅ `concurrencyPolicy: Forbid` 已设置
- [ ] ✅ `activeDeadlineSeconds` 已设置（建议 10800 秒 = 3 小时）
- [ ] ✅ 文件锁已实现并测试
- [ ] ✅ 测试手动删除锁文件后能正常恢复
- [ ] ✅ 配置了企微通知（失败告警）
- [ ] ✅ 限制了 Job 历史记录数量
- [ ] ✅ 设置了合理的资源限制（CPU/内存）

---

## 总结

**多重防护机制**：

1. ✅ **Kubernetes 层**: `concurrencyPolicy: Forbid`
2. ✅ **应用层**: 文件锁（`/tmp/iotdb-restore.lock`）
3. ✅ **超时保护**: `activeDeadlineSeconds: 7200`

**推荐配置**：

- 执行频率：每 2-3 小时
- 任务耗时：~1 小时
- 安全缓冲：1-2 小时

**结果**：无并发冲突，稳定可靠！ 🎉
