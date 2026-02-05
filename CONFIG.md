# 配置说明

## 敏感信息配置

本项目的配置文件包含敏感信息（如 Kubernetes 凭证、企微 Webhook URL 等）。为了安全起见，请按以下步骤配置：

### 1. 配置文件结构

```
configs/
├── config.example.yaml    # 配置模板（可提交到 Git）
├── config.yaml            # 默认配置（不要包含真实密钥）
└── config.local.yaml      # 本地配置（包含真实密钥，不要提交）
```

### 2. 快速配置

#### 方法 1: 修改默认配置（不推荐提交到 Git）

```bash
# 直接编辑配置文件
vim configs/config.yaml

# 修改以下敏感信息：
# 1. kubeconfig: 指向您的 kubeconfig 文件
# 2. notification.wechat.webhook_url: 您的企微 Webhook URL
# 3. backup.base_url: 您的 OSS 地址
```

#### 方法 2: 创建本地配置（推荐）

```bash
# 1. 复制示例配置
cp configs/config.example.yaml configs/config.local.yaml

# 2. 编辑本地配置，填写真实信息
vim configs/config.local.yaml

# 3. 使用本地配置运行
./bin/iotdb-restore -c configs/config.local.yaml restore
```

### 3. 敏感配置项

#### Kubernetes 凭证

```yaml
kubernetes:
  kubeconfig: /path/to/.kube/config  # 或 ~/.kube/config
  # 注意: 不要将 kubeconfig 文件提交到 Git
```

#### 企业微信 Webhook URL

```yaml
notification:
  wechat:
    webhook_url: https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=YOUR_KEY
    # 获取方式: 企业微信管理后台 -> 群聊 -> 群机器人 -> 添加机器人
```

#### OSS 备份地址

```yaml
backup:
  base_url: https://your-bucket.oss-accelerate.aliyuncs.com/your-path
  # 可能包含访问凭证信息
```

#### IoTDB 凭证

```yaml
iotdb:
  username: root
  password: root  # 如果修改过默认密码
```

### 4. Git 安全检查

确认以下文件不会被提交：

```bash
# 检查 .gitignore
cat .gitignore | grep -E "(kubeconfig|secret|\.local\.yaml)"

# 确认这些路径被忽略：
# *.kubeconfig
# .kube/config
# *.local.yaml
# deployments/k8s/*secret*.yaml
```

### 5. 已经提交的敏感信息？如果已经不小心提交了敏感信息：

```bash
# 1. 立即修改密钥/密码
# 2. 从 Git 历史中移除敏感文件
git filter-branch --tree-filter 'rm -f configs/config.local.yaml' HEAD

# 3. 强制推送（谨慎使用）
git push origin --force --all
```

### 6. Kubernetes Secrets 管理

在 Kubernetes 中部署时，使用 Secret 存储敏感信息：

```bash
# 创建 kubeconfig Secret
kubectl create secret generic kube-config \
  --from-file=kubeconfig=/path/to/.kube/config \
  -n iotdb

# 在 deployment 中引用
# volumes:
# - name: kubeconfig
#   secret:
#     secretName: kube-config
```

### 7. 环境变量方式

也可以通过环境变量传递配置：

```bash
export IOTDB_KUBECONFIG=/path/to/.kube/config
export WECHAT_WEBHOOK_URL=https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx

./bin/iotdb-restore restore
```

### 8. 配置文件优先级

```
命令行参数 > 环境变量 > 配置文件 > 默认值
```

---

## 安全建议

1. ✅ **永远不要**将包含真实密钥的配置文件提交到 Git
2. ✅ 使用 `config.local.yaml` 或 `config.prod.yaml` 存储生产配置
3. ✅ 定期轮换密钥和密码
4. ✅ 使用 Kubernetes Secrets 存储集群凭证
5. ✅ 限制配置文件的访问权限（`chmod 600 config.yaml`）
6. ✅ 定期审计 Git 历史，检查是否有敏感信息泄露

---

## 配置检查清单

部署前请确认：

- [ ] 已检查 `.gitignore` 是否包含敏感文件路径
- [ ] 已使用占位符替换 `config.yaml` 中的真实密钥
- [ ] 已创建 `config.local.yaml` 用于本地开发
- [ ] 已使用 Kubernetes Secrets 管理集群凭证
- [ ] 已限制配置文件权限：`chmod 600 configs/config*.yaml`
- [ ] 已验证 `git status` 没有显示敏感文件
