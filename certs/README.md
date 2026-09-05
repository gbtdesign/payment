# certs/ 证书目录说明

本目录用于存放微信支付商户 API 证书与私钥。**除本文件外，目录内的一切内容都已被 gitignore。**

## 为什么单独设一个目录

`apiclient_key.pem` 是商户私钥，配合商户号即可发起退款、代金券等资金操作。它的泄漏等级等同于商户账户密码：

- 一旦提交进仓库，即使立刻删除，它仍留在 git 历史中，只能到商户平台**作废证书并重新申请**
- 一旦被打包进镜像层并推送到镜像仓库，所有能 pull 该镜像的人都拿到了它

因此本目录整体忽略、且 Dockerfile 与 .dockerignore 都显式排除 `certs/`，三层防护缺一不可。

## 需要放什么

| 文件 | 用途 | 获取途径 |
|---|---|---|
| `apiclient_key.pem` | 商户私钥，用于请求签名与生成前端调起参数 | 商户平台「账户中心 → API 安全 → 申请 API 证书」，用证书工具生成，**只能下载一次** |
| `apiclient_cert.pem` | 商户证书（含公钥），部分老接口需要 | 同上，与私钥一同产出 |
| `wechatpay_public_key.pem` | 微信支付公钥，用于回调验签（公钥模式） | 商户平台「API 安全 → 微信支付公钥」下载 |

平台证书模式下无需手工下载平台证书，官方 SDK 的 `AutoAuthCipher` 会自动拉取并轮换。

## 怎么使用

证书不进仓库、不进镜像，只通过部署环境注入：

```bash
# 本地开发：把文件放进本目录，再在 .env 中指向它
WECHATPAY_PRIVATE_KEY_PATH=certs/apiclient_key.pem
```

```bash
# Docker：只读挂载，不要 COPY 进镜像
docker run -v "$PWD/certs:/app/certs:ro" --env-file .env payment-server
```

```yaml
# Kubernetes：用 Secret 挂载，Secret 由运维在集群内创建，不落任何仓库
volumes:
  - name: wechatpay-certs
    secret:
      secretName: wechatpay-apiclient
      defaultMode: 0400
```

## 自查清单

提交代码前确认：

```bash
git status --short certs/    # 期望只看到 README.md，不应出现 .pem / .key
git check-ignore -v certs/apiclient_key.pem   # 期望命中 .gitignore 规则
```

若不小心已经提交过真实私钥，处理顺序是：**先到商户平台作废证书，再清理 git 历史**。反过来做没有意义——历史被强推覆盖前，私钥已经外泄。

证书相关的接入步骤见 [docs/wechatpay-checklist.md](../docs/wechatpay-checklist.md)。
