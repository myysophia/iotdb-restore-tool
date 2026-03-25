# IoTDB DataNode 注册冲突问题排查记录

**日期**: 2026-03-23
**问题**: `iotdb-datanode-0` 启动失败，Pod 进入 `CrashLoopBackOff`
**状态**: ✅ 已恢复

---

## 一、问题现象

### 1.1 Pod 状态

```bash
kubectl --kubeconfig ~/.kube/config-admin -n iotdb get po -o wide
```

初始状态：

```text
NAME                 READY   STATUS             RESTARTS
iotdb-confignode-0   1/1     Running            0
iotdb-datanode-0     0/1     CrashLoopBackOff   2
```

### 1.2 DataNode 启动日志

`iotdb-datanode-0` 在启动后很快退出，退出码为 `255`。关键日志如下：

```text
Reject DataNode registration. Because the following ip:port:
[TEndPoint(ip:iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local, port:10730),
 TEndPoint(ip:iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local, port:10760),
 TEndPoint(ip:iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local, port:10740),
 TEndPoint(ip:iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local, port:10750)]
of the current DataNode is conflicted with other registered Nodes in the cluster.
```

### 1.3 ConfigNode 返回结果

`iotdb-confignode-0` 同时记录了注册拒绝：

```text
TDataNodeRegisterResp(status:TSStatus(code:1002, message:Reject DataNode registration...))
```

这说明故障不是容器镜像、资源不足、PVC 挂载失败或探针问题，而是 **集群元数据中的节点注册信息发生冲突**。

---

## 二、根因结论

### 2.1 直接根因

`ConfigNode` 中保留了旧的 `DataNode` 注册元数据，而新的 `iotdb-datanode-0` 启动时尝试以相同地址重新注册，导致注册冲突并退出。

冲突的节点地址为：

- `iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local:6667`
- `iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local:10730`
- `iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local:10740`
- `iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local:10750`
- `iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local:10760`

### 2.2 本次残留的具体元数据

这次残留的不是业务数据，也不是 Schema 定义本身，而是 **集群成员拓扑元数据**：

- `ConfigNode PVC` 中的共识日志里，保存了一条旧的 `DataNodeLocation`
- `DataNode PVC` 中的本地系统文件里，保存了该节点的持久化身份信息

对应关系如下：

| 元数据位置 | 内容 | 说明 |
|-----------|------|------|
| `/iotdb/data/confignode/consensus/.../current/log_inprogress_0` | 旧的 `DataNodeLocation` | 集群侧持久化的节点注册记录 |
| `/iotdb/data/datanode/system/system.properties` | `data_node_id=1` 和各端口配置 | 节点本地持久化身份 |

---

## 三、证据链

### 3.1 StatefulSet 和 PVC 是旧资源

```bash
kubectl --kubeconfig ~/.kube/config-admin -n iotdb get sts -o wide
kubectl --kubeconfig ~/.kube/config-admin -n iotdb get pvc -o wide
```

观察结果：

- `iotdb-confignode` StatefulSet 已存在 `59d`
- `iotdb-datanode` StatefulSet 已存在 `59d`
- 两个 PVC 也都已经存在 `59d`

这说明当天重启的是旧集群资源，不是一次干净的新建部署。

### 3.2 DataNode 本地系统元数据

在 `iotdb-datanode-0` 中检查：

```bash
kubectl --kubeconfig ~/.kube/config-admin -n iotdb exec iotdb-datanode-0 -- \
  sed -n '1,240p' /iotdb/data/datanode/system/system.properties
```

关键内容：

```properties
data_node_id=1
dn_rpc_address=iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local
dn_rpc_port=6667
dn_internal_address=iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local
dn_internal_port=10730
dn_mpp_data_exchange_port=10740
dn_schema_region_consensus_port=10750
dn_data_region_consensus_port=10760
```

这说明 `DataNode PVC` 中已经保存了历史节点身份，节点 ID 为 `1`。

### 3.3 ConfigNode 共识日志中的旧注册记录

在 `iotdb-confignode-0` 中检查：

```bash
kubectl --kubeconfig ~/.kube/config-admin -n iotdb exec iotdb-confignode-0 -- \
  grep -abo "iotdb-datanode-0" \
  /iotdb/data/confignode/consensus/47474747-4747-4747-4747-000000000000/current/log_inprogress_0
```

可以在共识日志中多次命中 `iotdb-datanode-0`，并进一步解析出完整的旧节点地址和端口：

- `iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local`
- `6667`
- `10730`
- `10740`
- `10750`
- `10760`

这证明 `ConfigNode` 的持久化元数据里确实保留了该节点的历史注册记录。

### 3.4 ConfigNode 日志中的恢复现象

在稍后的运行日志中，`ConfigNode` 记录到：

```text
Execute RegisterDataNodeRequest ... dataNodeId:1 ...
status:TSStatus(code:200, message:Accept Node registration.)
```

这说明后续 `DataNode` 不是以“全新节点”身份加入，而是以持久化的 `data_node_id=1` 重新注册，并被接受。

---

## 四、为什么一开始会失败，后来又恢复

### 4.1 失败原因

最开始 `DataNode` 启动时，`ConfigNode` 认为当前注册请求与集群中已有节点冲突，因此返回 `code:1002`，导致容器退出。

### 4.2 后续恢复原因

后续重新启动时，`DataNode` 使用了持久化文件中的原始节点身份：

- `data_node_id=1`
- 原地址 `iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local`
- 原端口 `6667/10730/10740/10750/10760`

因此 `ConfigNode` 将其识别为原有节点重新上线，而不是新的重复节点，最终接受注册。

---

## 五、影响范围判断

### 5.1 受影响的内容

- `iotdb-datanode-0` 启动过程
- 集群成员注册与心跳恢复
- 恢复任务执行前的 IoTDB 可用性

### 5.2 未直接损坏的内容

- 已恢复的业务数据本身
- `DataNode` 本地数据库目录
- `ConfigNode` 本地集群配置文件

本次问题的本质是 **节点拓扑元数据恢复过程中的一致性问题**，不是数据文件损坏。

---

## 六、处理建议

### 6.1 如果要保留现有恢复结果

当前 Pod 已恢复正常：

```text
iotdb-confignode-0   1/1 Running
iotdb-datanode-0     1/1 Running
```

建议继续保留现有 PVC，不要做破坏性清理，只做一致性检查：

- 检查 `show cluster details`
- 检查是否还存在无法解析或已经下线的历史节点
- 检查 `confignode` 日志里是否持续出现 `UnresolvedAddressException`

### 6.2 如果以后要做“全新重建”

如果目标是彻底重建 IoTDB 集群，而不是复用旧集群身份，则必须同时清理：

- `ConfigNode` PVC 中的集群拓扑元数据
- `DataNode` PVC 中的本地节点身份元数据

只删 Pod、不删 PVC，会导致旧的 `nodeId` 和旧注册信息继续保留，从而再次引发类似冲突。

---

## 七、经验总结

1. `CrashLoopBackOff` 不一定是资源问题，IoTDB 集群启动失败首先要看注册日志。
2. `ConfigNode` 和 `DataNode` 的 PVC 都会保存集群身份信息，不能只把它们当成普通数据盘。
3. 在“恢复数据”和“重建集群”两个场景之间，必须明确是否复用旧的节点身份。
4. 如果要复用旧数据盘，`ConfigNode` 和 `DataNode` 的持久化元数据必须保持一致。
5. 如果要做全新部署，必须成套清理集群元数据，否则容易出现节点注册冲突。

---

## 八、相关命令

```bash
# 查看 Pod 状态
kubectl --kubeconfig ~/.kube/config-admin -n iotdb get po -o wide

# 查看 datanode 日志
kubectl --kubeconfig ~/.kube/config-admin -n iotdb logs iotdb-datanode-0 --tail=200

# 查看 confignode 日志
kubectl --kubeconfig ~/.kube/config-admin -n iotdb logs iotdb-confignode-0 --tail=200

# 查看 datanode 本地系统元数据
kubectl --kubeconfig ~/.kube/config-admin -n iotdb exec iotdb-datanode-0 -- \
  sed -n '1,240p' /iotdb/data/datanode/system/system.properties

# 搜索 confignode 共识日志中的旧 datanode 记录
kubectl --kubeconfig ~/.kube/config-admin -n iotdb exec iotdb-confignode-0 -- \
  grep -abo "iotdb-datanode-0" \
  /iotdb/data/confignode/consensus/47474747-4747-4747-4747-000000000000/current/log_inprogress_0
```
