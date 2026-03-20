# IoTDB DataRegion 问题快速参考

## 快速诊断

```bash
# 1. 检查错误类型
grep "exit code" /var/log/iotdb-restore.log | sort | uniq -c

# 2. 检查成功率
grep "恢复操作完成" /var/log/iotdb-restore.log | tail -5

# 3. 检查 DataRegion
kubectl exec -n iotdb iotdb-datanode-0 -- /iotdb/sbin/start-cli.sh -h iotdb-datanode -e "show data regions"
```

## 错误类型对照表

| 错误码 | 原因 | 解决方案 |
|-------|------|---------|
| **137** | OOM 内存不足 | 增加内存限制（8Gi → 10Gi） |
| **OCI runtime failed** | Pod 重启 | 检查 Pod 健康状态 |
| **1: 301 共识组错误** | DataRegion 不存在 | 等待 20 分钟或预创建 Schema |

## DataRegion 状态检查

```bash
# 查看所有 DataRegion
kubectl exec -n iotdb iotdb-datanode-0 -- /iotdb/sbin/start-cli.sh -h iotdb-datanode -e "show data regions"

# 查看 TimeSlotNum（导入的数据量）
# └─ 如果 TimeSlotNum = 0，说明没有数据导入

# 查看 CreateTime
# └─ 如果是最近创建的，说明正在初始化
```

## 常见场景处理

### 场景 1：首次恢复成功率低（<30%）

**症状**：
```
恢复操作完成: {"success_count": 183, "failed_count": 561}
```

**处理**：
```bash
# 等待 20 分钟
sleep 1200

# 检查 DataRegion 是否创建
kubectl exec -n iotdb iotdb-datanode-0 -- /iotdb/sbin/start-cli.sh -h iotdb-datanode -e "show data regions"

# 第二次恢复会自动成功
```

### 场景 2：持续失败

**检查**：
```bash
# 1. 检查 Pod 内存
kubectl top pod -n iotdb iotdb-datanode-0

# 2. 检查 DataRegion
kubectl exec -n iotdb iotdb-datanode-0 -- /iotdb/sbin/start-cli.sh -h iotdb-datanode -e "show data regions"

# 3. 手动测试
kubectl exec -n iotdb iotdb-datanode-0 -- bash -c "
  /iotdb/sbin/start-cli.sh -h iotdb-datanode \
    -e \"load '/iotdb/data/.../file.tsfile' verify=false\"
"
```

## DataRegion 创建时间线

```
第一次恢复（00:43）
├─ 导入 183 个元数据文件
└─ 导入 561 个数据文件失败

[20 分钟延迟]

DataRegion 创建（01:37）
├─ ConfigNode 分配 DataRegion ID
├─ Leader 选举
└─ 初始化完成

第二次恢复（03:43）
└─ 成功率提升 ✅
```

## 关键要点

1. ✅ **失败 ≠ 真的失败** - 可能是准备阶段
2. ✅ **等待重试** - CronJob 会自动重试
3. ✅ **耐心等待** - DataRegion 创建需要 20 分钟
4. ✅ **监控日志** - 观察成功率变化

## 预防措施

```yaml
# 配置建议
import:
  concurrency: 1        # 降低并发
  batch_size: 3         # 批次处理
  batch_pause: true     # 批次间暂停
  batch_delay: 3        # 暂停 3 秒

# CronJob 频率
# 每 2 小时运行一次，给 DataRegion 充足的创建时间
schedule: "43 */2 * * *"
```

## 相关文档

- [详细排查记录](./troubleshooting-dataregion-mismatch-20260320.md)
- [内存优化记录](./memory-tuning-20260319.md)
- [IoTDB 官方文档](https://iotdb.apache.org/)
