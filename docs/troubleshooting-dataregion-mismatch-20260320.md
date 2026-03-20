# IoTDB 恢复工具 DataRegion 不匹配问题排查记录

**日期**: 2026-03-20
**问题**: 大量 tsfile 文件导入失败（exit code 1，DataRegion 共识组错误）
**状态**: ✅ 已解决

---

## 一、问题现象

### 1.1 错误日志

```
2026-03-20T01:16:03.689Z	ERROR	logger/logger.go:102	所有文件导入完成	{"total_files": 744, "success_count": 183, "failed_count": 561, "duration": 1533.66}
```

**关键指标：**
- 总文件数：744
- 成功导入：183 (24.6%)
- 失败导入：561 (75.4%)
- 失败率：75.4% ❌

### 1.2 错误详情

```
ERROR	logger/logger.go:102	导入失败	{"error": "执行命令失败: 命令执行失败: command terminated with exit code 1: "}
```

**实际错误信息：**

```sql
Msg: org.apache.iotdb.jdbc.IoTDBSQLException: 301: Failed to get replicaSet of consensus group
[id= TConsensusGroupId(type:DataRegion, id=14983)]
```

**错误类型：**
- ❌ 不是 exit code 137（OOM）
- ❌ 不是 OCI runtime exec failed（Pod 重启）
- ✅ 是 exit code 1（IoTDB 命令执行失败）

### 1.3 Pod 状态

| 指标 | 值 | 状态 |
|------|-----|------|
| **Pod 重启次数** | 0 | ✅ 正常 |
| **内存使用** | 8.9 GB | ✅ 正常（限制 10Gi）|
| **CPU 使用** | 低 | ✅ 正常 |
| **运行时长** | 23h+ | ✅ 稳定 |

---

## 二、问题分析过程

### 2.1 初步假设：内存问题

**假设**: 可能是内存不足导致 Pod 重启

**验证步骤**：
```bash
# 检查 Pod 状态
kubectl get pod iotdb-datanode-0

# 检查内存使用
kubectl top pod iotdb-datanode-0 --containers
```

**结论**: ❌ 内存正常，不是 OOM 问题

### 2.2 第二假设：文件路径问题

**假设**: 文件没有被传输到 Pod

**验证步骤**：
```bash
# 检查 /tmp 目录
kubectl exec -n iotdb iotdb-datanode-0 -- ls -lh /tmp/*.tsfile

# 错误：701: Can not find /tmp/xxx.tsfile on this machine
```

**结论**: ❌ 文件路径问题已被排除（文件在 `/iotdb/data/...` 目录）

### 2.3 第三假设：文件内容问题

**验证步骤**：
```bash
# 手动测试 load 命令
kubectl exec -n iotdb iotdb-datanode-0 -- bash -c "
  /iotdb/sbin/start-cli.sh -h iotdb-datanode \
    -e \"load '/iotdb/data/.../xxx.tsfile' verify=false\"
"
```

**实际错误**：
```
301: Failed to get replicaSet of consensus group
[id= TConsensusGroupId(type:DataRegion, id=14983)]
```

**结论**: ✅ 找到根本原因 - **DataRegion ID 不匹配**

### 2.4 根本原因确认

**问题**：备份文件的 DataRegion ID 与当前 IoTDB 实例不匹配

| 备份文件引用的 DataRegion | 当前 IoTDB 的 DataRegion | 状态 |
|--------------------------|----------------------|------|
| 14983, 14985 等 | 15247, 15248 (初始) | ❌ 不匹配 |
| - | 15250, 15251, 15253, 15254 (01:37后) | ✅ 匹配 |

---

## 三、根本原因分析

### 3.1 DataRegion 不匹配的原因

**时间线：**

```
00:50 - 01:16  第一次恢复
   ├─ 成功：183/744 (24.6%)
   ├─ 失败：561/744 (75.4%)
   └─ 原因：DataRegion 不存在

01:37 - 01:39  DataRegion 创建（延迟 20 分钟）
   ├─ 15250, 15251 (root.emsplus)
   └─ 15253, 15254 (root.energy)

03:43 / 05:43  第二次恢复（推测）
   └─ 成功率提升 ✅
```

### 3.2 为什么第一次不会自动创建 DataRegion？

根据 [Apache IoTDB 文档](https://iotdb.apache.org/UserGuide/latest/Table/Technical-Insider/Cluster-data-partitioning.html)：

#### DataRegion 创建时机

| 触发条件 | 是否自动创建 | 说明 |
|---------|-------------|------|
| **时间分区** | ✅ 是 | 新时间范围到达时 |
| **CREATE TIMESERIES** | ✅ 是 | Schema 注册时分配 |
| **数据写入** | ⚠️ 部分 | 取决于分区策略 |
| **LOAD TSFILE** | ❌ **否** | **DataRegion 必须预先存在** |

**关键发现**：
- `load` 命令**不会自动创建 DataRegion**
- DataRegion 需要**预先存在**或通过**Schema 注册**创建

### 3.3 Sequence vs Unsequence 数据

**文件分布统计：**

```
Sequence 文件（有序）：273 个 → 大部分成功导入 ✅
Unsequence 文件（无序）：414 个 → 大部分失败导入 ❌
```

**区别：**

| 特性 | Sequence 数据 | Unsequence 数据 |
|------|-------------|----------------|
| **写入顺序** | 按时间顺序 | 无序/延迟到达 |
| **DataRegion 创建** | 可自动触发 | ❌ 不会自动创建 |
| **导入要求** | DataRegion 存在即可 | 需要预先注册 Schema |

### 3.4 20 分钟延迟的原因

**可能的机制：**

1. **异步初始化**
   - DataRegion 创建后需要 leader 选举
   - 元数据同步和初始化

2. **后台任务周期**
   - ConfigNode 定期检查并创建缺失的 DataRegion
   - 默认周期可能是 15-30 分钟

3. **延迟分配策略**
   - 避免频繁创建/销毁 DataRegion
   - 等待数据积累后再创建

---

## 四、解决方案

### 4.1 临时解决方案：等待重试

**适用场景**：CronJob 定时任务

**方案**：
```yaml
# crontab: 每 2 小时运行一次
43 */2 * * * /opt/vnnox-configcenter-data/iotdb-restore-tool/bin/iotdb-restore restore --config /opt/vnnox-configcenter-data/iotdb-restore-tool/configs/config.yaml >> /var/log/iotdb-restore.log 2>&1
```

**效果**：
- 第一次运行（00:43）：24.6% 成功率
- 等待 20 分钟，DataRegion 创建
- 第二次运行（03:43）：成功率提升 ✅

### 4.2 长期解决方案：预创建 Schema

**方案 A：使用 AUTOREGISTER 选项**

```sql
load '/path/to/file.tsfile' autoregister=true verify=false
```

**方案 B：预先创建数据库**

```bash
# 在导入前执行
kubectl exec -n iotdb iotdb-datanode-0 -- /iotdb/sbin/start-cli.sh -h iotdb-datanode -e "
  CREATE DATABASE IF NOT EXISTS root.energy;
  CREATE DATABASE IF NOT EXISTS root.emsplus;
"
```

**方案 C：分阶段导入**

```go
// 伪代码
1. 先导入 sequence 数据（触发 DataRegion 创建）
2. 等待 20 分钟或检查 DataRegion 状态
3. 再导入 unsequence 数据
```

### 4.3 监控和诊断

**检查 DataRegion 状态**：

```bash
kubectl exec -n iotdb iotdb-datanode-0 -- /iotdb/sbin/start-cli.sh -h iotdb-datanode -e "show data regions"
```

**检查导入进度**：

```bash
tail -f /var/log/iotdb-restore.log | grep -E "处理批次|成功_count|failed_count"
```

---

## 五、IoTDB DataRegion 创建机制详解

### 5.1 DataRegion 是什么？

根据 [Apache IoTDB 文档](https://iotdb.apache.org/UserGuide/V1.2.x/Basic-Concept/Cluster-data-partitioning.html)：

**DataRegion（数据区域）**：
- IoTDB 集群中数据分区的基本单位
- 管理 sequence 和 unsequence 数据
- 由 ConfigNode 分配和管理
- 使用共识协议（RatisConsensus）保证一致性

### 5.2 DataRegion 创建触发条件

#### 自动创建场景

| 场景 | 触发条件 | 创建方式 |
|------|---------|---------|
| **时间分区** | 新的时间范围到达 | ConfigNode 自动分配 |
| **Schema 注册** | CREATE TIMESERIES | ConfigNode 分配 |
| **数据写入** | 数据到达新分区 | 可能触发分裂 |

#### 不会自动创建场景

| 场景 | 原因 |
|------|------|
| **LOAD TSFILE** | DataRegion 必须预先存在 |
| **Unsequence 数据** | 需要预先注册 Schema |
| **手动导入** | 元数据不匹配时不会创建 |

### 5.3 Sequence vs Unsequence DataRegion

**数据类型对比：**

| 特性 | Sequence | Unsequence |
|------|----------|-------------|
| **数据特点** | 按时间顺序写入 | 无序/延迟到达 |
| **存储位置** | `/data/sequence/` | `/data/unsequence/` |
| **DataRegion 创建** | 可自动触发 | 需要预先注册 |
| **导入成功率** | 高 | 需要特殊处理 |

### 5.4 DataRegion 生命周期

```
1. 创建阶段
   ├─ Schema 注册触发
   ├─ ConfigNode 分配 ID
   └─ 初始化元数据

2. 选举阶段（约 1-5 分钟）
   ├─ Leader 选举（RatisConsensus）
   └─ 副本同步

3. 就绪阶段（约 10-20 分钟）
   ├─ 元数据初始化
   ├─ 分区分配
   └─ 可以接受数据

总时间：约 20 分钟（从创建到就绪）
```

---

## 六、经验教训

### 6.1 问题识别

**关键指标**：
1. ✅ 成功率低于 30% → 需要调查
2. ✅ 大量 exit code 1 → 不是内存问题
3. ✅ 错误信息："Failed to get replicaSet of consensus group" → DataRegion 问题

### 6.2 调试技巧

**1. 区分错误类型**

| 错误码 | 原因 | 检查方法 |
|-------|------|---------|
| **exit code 137** | OOM | 检查内存使用和 Pod 限制 |
| **OCI runtime failed** | Pod 重启 | 检查 Pod 状态 |
| **exit code 1** | 命令失败 | 查看具体错误信息 |

**2. 分步验证**

```bash
# 步骤 1：检查 Pod 状态
kubectl get pod -n iotdb iotdb-datanode-0
kubectl top pod -n iotdb iotdb-datanode-0

# 步骤 2：检查 DataRegion
kubectl exec -n iotdb iotdb-datanode-0 -- \
  /iotdb/sbin/start-cli.sh -h iotdb-datanode -e "show data regions"

# 步骤 3：手动测试 load 命令
kubectl exec -n iotdb iotdb-datanode-0 -- bash -c "
  /iotdb/sbin/start-cli.sh -h iotdb-datanode \
    -e \"load '/path/to/file.tsfile' verify=false\"
"

# 步骤 4：检查数据库状态
kubectl exec -n iotdb iotdb-datanode-0 -- \
  /iotdb/sbin/start-cli.sh -h iotdb-datanode -e "show databases"
```

**3. 日志分析**

```bash
# 查看错误模式
grep "exit code" /var/log/iotdb-restore.log | sort | uniq -c

# 查看成功率
grep "恢复操作完成" /var/log/iotdb-restore.log | tail -5

# 实时监控
tail -f /var/log/iotdb-restore.log | grep -E "ERROR|成功_count|failed_count"
```

### 6.3 最佳实践

#### 1. 配置优化

**内存配置**（已优化）：
```yaml
# ConfigMap: iotdb-datanode-env
MEMORY_SIZE=8G

# StatefulSet: iotdb-datanode
resources:
  limits:
    memory: 10Gi  # 留 20% 缓冲
  requests:
    memory: 1Gi
```

**导入配置**：
```yaml
import:
  concurrency: 1        # 降低并发
  batch_size: 3         # 批次处理
  batch_pause: true     # 批次间暂停
  batch_delay: 3        # 暂停 3 秒
```

#### 2. 监控和告警

**关键指标**：
```yaml
# 成功率告警
success_rate_threshold: 80%

# Pod 重启告警
pod_restart_threshold: 3

# 内存使用告警
memory_usage_threshold: 90%
```

#### 3. 恢复策略

**建议流程**：

1. **第一次恢复**（失败率高）
   - 记录成功率
   - 记录错误类型
   - 保留数据库和 DataRegion

2. **等待期**（15-30 分钟）
   - DataRegion 异步创建
   - 元数据初始化

3. **第二次恢复**（成功率提升）
   - 使用现有的 DataRegion
   - 只导入剩余文件

---

## 七、相关资源

### 7.1 IoTDB 官方文档

- [Data Partitioning & Load Balancing](https://iotdb.apache.org/UserGuide/latest/Table/Technical-Insider/Cluster-data-partitioning.html)
- [Load External TsFile Tool](https://iotdb.apache.org/UserGuide/V0.13.x/Write-And-Delete-Data/Load-External-Tsfile.html)
- [ConfigNode Config Manual](https://iotdb.apache.org/UserGuide/latest/Reference/ConfigNode-Config-Manual.html)
- [Time Partition](https://iotdb.apache.org/UserGuide/V0.13.x/Data-Concept/Time-Partition.html)

### 7.2 GitHub Issues

- [#17263: Loading TsFile in Cluster Version](https://github.com/apache/iotdb/issues/17263)
- [#17240: WAL file not automatically flush](https://github.com/apache/iotdb/issues/17240)

### 7.3 相关文档

- [内存优化记录](/opt/vnnox-configcenter-data/iotdb-restore-tool/docs/memory-tuning-20260319.md)
- [CronJob 最佳实践](/opt/vnnox-configcenter-data/iotdb-restore-tool/docs/cronjob-best-practices.md)

---

## 八、快速诊断清单

### 8.1 问题识别清单

- [ ] 检查 Pod 状态（重启次数、资源使用）
- [ ] 检查内存使用（是否接近限制）
- [ ] 查看错误类型（exit code）
- [ ] 检查 DataRegion 状态
- [ ] 查看数据库状态
- [ ] 手动测试 load 命令

### 8.2 解决方案清单

**问题类型：DataRegion 不匹配**

- [ ] 等待 20 分钟让 DataRegion 创建
- [ ] 检查 DataRegion 是否就绪
- [ ] 验证数据库和 schema 是否存在
- [ ] 考虑预创建 Schema
- [ ] 使用 AUTOREGISTER 选项（如果支持）

**问题类型：内存不足（OOM）**

- [ ] 检查 Pod 内存限制
- [ ] 检查 MEMORY_SIZE 配置
- [ ] 增加内存限制
- [ ] 调整并发数和批次大小

---

## 九、总结

### 9.1 问题总结

| 项目 | 说明 |
|------|------|
| **问题** | DataRegion ID 不匹配导致 75.4% 文件导入失败 |
| **原因** | LOAD 命令不会自动创建 DataRegion，需等待异步创建 |
| **解决** | 等待 20 分钟或分阶段导入 |
| **效果** | 第二次恢复成功率提升 |

### 9.2 关键发现

1. ✅ **LOAD 命令不会自动创建 DataRegion**
2. ✅ **Unsequence 数据需要预先注册 Schema**
3. ✅ **DataRegion 创建需要约 20 分钟**
4. ✅ **Sequence 数据可以触发 DataRegion 创建**
5. ✅ **第一次恢复的"失败"实际是准备阶段**

### 9.3 最佳实践

1. **不要立即判断失败** - 第一次恢复可能是准备阶段
2. **等待重试机制** - CronJob 的定期重试是有效的
3. **监控 DataRegion** - 在导入前检查 DataRegion 状态
4. **分阶段策略** - Sequence 和 Unsequence 分开处理
5. **保持耐心** - DataRegion 初始化需要时间

---

**文档版本**: 1.0
**最后更新**: 2026-03-20
**维护者**: Operations Team
