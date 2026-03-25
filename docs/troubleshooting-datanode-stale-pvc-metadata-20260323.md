# IoTDB DataNode 因旧 PVC 元数据残留导致启动异常排查记录

**日期**: 2026-03-23
**问题**: `iotdb-datanode-0` 启动时持续报错，无法正常加入当前 `iotdb` 集群
**状态**: 已定位根因，待按方案修复

---

## 一、问题现象

### 1.1 Kubernetes 表面状态正常，但服务实际不可用

```bash
kubectl --kubeconfig ~/.kube/config-admin -n iotdb get po -o wide
```

当时看到：

```text
NAME                 READY   STATUS    RESTARTS
iotdb-confignode-0   1/1     Running   0
iotdb-datanode-0     1/1     Running   0
```

但 `datanode` 容器里本地 CLI 连 `127.0.0.1:6667` 失败：

```bash
kubectl --kubeconfig ~/.kube/config-admin -n iotdb exec iotdb-datanode-0 -- \
  sh -lc '/iotdb/sbin/start-cli.sh -h 127.0.0.1 -p 6667 -e "show version"'
```

返回：

```text
ErrorCan't execute sql becauseConnection Error, please check whether the network is available or the server has started.
```

这说明：

- 容器进程在跑
- Java 进程也存在
- 但 IoTDB `DataNode` 服务没有真正完成启动

### 1.2 DataNode 启动日志持续报错

`iotdb-datanode-0` 日志中从启动开始反复出现：

```text
Failed to connect to ConfigNode
TEndPoint(ip:iotdb-confignode-0.iotdb-confignode.ems-au.svc.cluster.local, port:10710)
```

以及核心异常：

```text
org.apache.thrift.protocol.TProtocolException:
Required field 'schemaRegionGrpcLeaderOutstandingAppendsMax' was not found in serialized data
```

同时伴随：

```text
Cannot pull system configurations from ConfigNode-leader
Fail to connect to any config node
```

---

## 二、根因结论

### 2.1 直接根因

`datanode` 启动时优先读取了 PVC 中残留的旧集群本地元数据，而不是使用当前容器环境中的新 `dn_seed_config_node`。

这些残留元数据把它指向了旧环境：

- 旧命名空间/域名: `ems-au`
- 旧集群版本信息: `1.1.2`

当前正在运行的容器镜像则是：

- `apache/iotdb:1.3.2-datanode`

所以出现了：

```text
当前 1.3.2 的 DataNode
去连接旧环境 ems-au 的 ConfigNode/旧元数据
=> 反序列化系统配置时字段不兼容
=> 报 TProtocolException
=> DataNode 无法完成初始化
```

### 2.2 这次残留的本质

这次问题的本质不是 Kubernetes 网络问题，也不是 Pod 调度问题，而是：

**DataNode PVC 中残留了旧集群身份和旧 ConfigNode 地址。**

---

## 三、证据链

### 3.1 当前集群的 ConfigNode 是新的 `iotdb / 1.3.2`

`confignode` 持久化文件：

```bash
kubectl --kubeconfig ~/.kube/config-admin -n iotdb exec iotdb-confignode-0 -- \
  sh -lc 'sed -n "1,240p" /iotdb/data/confignode/system/confignode-system.properties'
```

关键内容：

```properties
cn_internal_address=iotdb-confignode-0.iotdb-confignode.iotdb.svc.cluster.local
config_node_list=0,iotdb-confignode-0.iotdb-confignode.iotdb.svc.cluster.local:10710,...
iotdb_version=1.3.2
```

说明当前 `confignode` 属于现在这套 `iotdb` 集群。

### 3.2 DataNode PVC 中保留的是旧环境 `ems-au / 1.1.2`

`datanode` 持久化文件：

```bash
kubectl --kubeconfig ~/.kube/config-admin -n iotdb exec iotdb-datanode-0 -- \
  sh -lc 'sed -n "1,240p" /iotdb/data/datanode/system/system.properties'
```

关键内容：

```properties
dn_internal_address=iotdb-datanode-0.iotdb-datanode.ems-au.svc.cluster.local
config_node_list=iotdb-confignode-0.iotdb-confignode.ems-au.svc.cluster.local:10710
dn_rpc_address=iotdb-datanode-0.iotdb-datanode.ems-au.svc.cluster.local
iotdb_version=1.1.2
data_node_id=1
```

这已经直接证明：

- 当前 `datanode` PVC 不是当前 `iotdb` 集群原生生成的
- 它带着旧环境 `ems-au` 的节点身份和旧 `ConfigNode` 地址

### 3.3 容器运行时配置其实是新的

同一个容器里的运行时配置 `/iotdb/conf/iotdb-datanode.properties` 则是当前环境：

```properties
dn_rpc_address=iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local
dn_internal_address=iotdb-datanode-0.iotdb-datanode.iotdb.svc.cluster.local
dn_seed_config_node=iotdb-confignode-0.iotdb-confignode.iotdb.svc.cluster.local:10710
```

说明问题不是镜像配置错误，而是：

**启动时优先使用了 PVC 中的旧持久化状态。**

### 3.4 日志明确显示它正在连接旧地址

`datanode` 日志里多次出现：

```text
Load ConfigNode successfully:
[TEndPoint(ip:iotdb-confignode-0.iotdb-confignode.ems-au.svc.cluster.local, port:10710)]
```

随后马上报：

```text
Failed to connect to ConfigNode ...
TProtocolException: Required field 'schemaRegionGrpcLeaderOutstandingAppendsMax' was not found
```

这说明它不是连错当前 `iotdb` 命名空间里的服务，而是被旧 `system.properties` 带到了旧环境。

---

## 四、PVC 中残留了哪些内容

### 4.1 目录体积

在 `datanode` PVC 中统计：

```text
124K  /iotdb/data/datanode/system
8.2M  /iotdb/data/datanode/consensus
1.3M  /iotdb/data/datanode/wal
4.0K  /iotdb/data/datanode/data
```

这个结果很重要：

- 主要残留集中在 `system / consensus / wal`
- `data` 目录几乎是空的

也就是说，这块盘当前更像是：

**旧节点身份盘 / 共识状态盘**

而不是一块承载大量当前有效业务数据的活跃数据盘。

### 4.2 残留位置

#### 核心残留 1: 本地节点身份

```text
/iotdb/data/datanode/system/system.properties
```

这是最直接把节点带偏的文件。

#### 核心残留 2: 旧 Region 共识状态

```text
/iotdb/data/datanode/consensus/schema_region/...
/iotdb/data/datanode/consensus/data_region/...
```

其中还能搜到旧地址：

```text
iotdb-datanode-0.iotdb-datanode.ems-au.svc.cluster.local:10750
```

说明不仅 `system.properties` 是旧的，连本地 consensus 状态也是旧集群留下来的。

#### 核心残留 3: WAL

```text
/iotdb/data/datanode/wal/...
```

WAL 中还能搜到旧版本痕迹：

```text
1.1.2
```

---

## 五、为什么会这样

IoTDB 集群模式下，`DataNode` 第一次成功加入集群后，会在本地持久化：

- `config_node_list`
- `data_node_id`
- 本地地址
- region / consensus 状态

后续重启时，它不会单纯依赖环境变量里的：

```properties
dn_seed_config_node
```

而会优先尝试使用本地持久化过的旧集群信息。

所以出现了典型现象：

```text
容器配置是新的
但 PVC 元数据是旧的
=> 启动逻辑优先走旧集群身份
=> 节点被带偏
```

---

## 六、修复建议

### 6.1 推荐方案: 只处理 DataNode PVC 的本地元数据

由于当前判断：

- `confignode` 是当前 `iotdb / 1.3.2`
- `datanode` PVC 明显是旧 `ems-au / 1.1.2`
- `/iotdb/data/datanode/data` 几乎为空

因此优先推荐：

**只清理 `datanode` 的本地身份元数据，不先动 `confignode`。**

推荐清理目标：

- `/iotdb/data/datanode/system`
- `/iotdb/data/datanode/consensus`
- `/iotdb/data/datanode/wal`

清理后重启 `iotdb-datanode-0`，让它按当前环境重新向当前 `confignode` 注册。

### 6.2 更重的方案: 直接删除 DataNode PVC

如果不想手动清目录，也可以直接删除 `datanode` PVC。

这种方式等价于把 `datanode` 本地身份、consensus、wal 一起清掉，简单直接。

### 6.3 最重方案: DataNode + ConfigNode PVC 一起删除

这也可以，但含义是：

**整套 IoTDB 集群按“全新集群”重建。**

适用于：

- 当前集群元数据不打算保留
- 之后准备重新恢复数据

如果只是解决这次 `datanode` 被旧 PVC 带偏的问题，不建议第一步就删 `confignode` PVC。

---

## 七、建议执行顺序

### 方案 A: 保守且推荐

1. 备份 `datanode` 的 `system/consensus/wal`
2. 清理这三个目录
3. 重启 `iotdb-datanode-0`
4. 观察它是否改为连接 `iotdb-confignode-0.iotdb-confignode.iotdb.svc.cluster.local`

### 方案 B: 更直接

1. 删除 `datanode` PVC
2. 重建 `datanode`
3. 观察是否正常加入当前集群

### 方案 C: 全新集群

1. 删除 `datanode` PVC
2. 删除 `confignode` PVC
3. 重新拉起整套 IoTDB
4. 重新恢复数据

---

## 八、经验总结

1. Pod `Running/Ready` 不等于 IoTDB 服务真正可用，仍需验证本地 `6667` 是否可连。
2. IoTDB 集群问题要区分“容器内配置”和“PVC 持久化身份”，后者优先级往往更高。
3. `system.properties` 残留旧 `config_node_list` 时，`DataNode` 会优先连接旧集群。
4. 只改运行时环境变量通常不够，因为本地 consensus 和 node identity 仍然可能是旧的。
5. 如果 `data` 目录基本为空，而 `system/consensus/wal` 明显是旧的，优先清本地元数据比直接删整套集群更稳。

---

## 九、相关命令

```bash
# 查看 datanode 当前日志
kubectl --kubeconfig ~/.kube/config-admin -n iotdb logs iotdb-datanode-0 --tail=200

# 查看 datanode 持久化身份
kubectl --kubeconfig ~/.kube/config-admin -n iotdb exec iotdb-datanode-0 -- \
  sh -lc 'sed -n "1,240p" /iotdb/data/datanode/system/system.properties'

# 查看 confignode 当前身份
kubectl --kubeconfig ~/.kube/config-admin -n iotdb exec iotdb-confignode-0 -- \
  sh -lc 'sed -n "1,240p" /iotdb/data/confignode/system/confignode-system.properties'

# 搜索旧环境痕迹
kubectl --kubeconfig ~/.kube/config-admin -n iotdb exec iotdb-datanode-0 -- \
  sh -lc 'grep -RIna "ems-au\|1.1.2" /iotdb/data/datanode 2>/dev/null | sed -n "1,120p"'

# 检查目录体积
kubectl --kubeconfig ~/.kube/config-admin -n iotdb exec iotdb-datanode-0 -- \
  sh -lc 'du -sh /iotdb/data/datanode/system /iotdb/data/datanode/consensus /iotdb/data/datanode/wal /iotdb/data/datanode/data 2>/dev/null'
```

---

## 十、相关文档

- [DataNode 注册冲突问题排查记录](./troubleshooting-datanode-registration-conflict-20260323.md)
- [Raft 在 IoTDB 中如何保证一致性](./raft-in-iotdb-explained-20260323.md)
