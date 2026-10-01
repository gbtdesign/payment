---
kind: build_system
name: Go 支付服务构建与镜像化流水线（Makefile + Dockerfile）
category: build_system
scope:
    - '**'
source_files:
    - Makefile
    - Dockerfile
    - .dockerignore
    - go.mod
    - go.sum
    - configs/config.yaml
    - cmd/server/main.go
---

## 1. 使用的系统与工具

- **语言与模块**：Go 单模块，依赖声明在 `go.mod` / `go.sum`。
- **构建入口**：根目录 `Makefile`，所有目标通过 `$(GO)` / `$(GOFMT)` 调用工具链，而非裸写 `go` / `gofmt`。工具路径优先取 PATH，回退到 `/usr/local/go/bin`，以适配本机 Go 未加入 PATH 的环境。
- **静态编译**：`CGO_ENABLED=0`，产物不依赖 glibc，可放入 alpine/scratch 运行镜像；二进制使用 `-trimpath -ldflags "-s -w"` 去掉绝对路径、压缩符号表。
- **容器化**：`Dockerfile` 采用两阶段构建（`golang:1.26-alpine` → `alpine:3.20`），运行镜像仅包含二进制、配置文件与非 root 用户。
- **代码质量**：`fmt-check`（`gofmt -l -s`）、`vet`（`go vet ./...`）、可选的 `golangci-lint run ./...`（未安装时跳过而不报错）。
- **测试**：`test` 与 `cover` 均带 `-race` 竞态检测；覆盖率输出到 `coverage.out` 并通过 `go tool cover -func` 打印汇总。

## 2. 关键文件

| 文件 | 作用 |
|---|---|
| `Makefile` | 统一构建、格式化、检查、测试、清理、镜像构建入口 |
| `Dockerfile` | 多阶段镜像构建，定义运行时安全基线 |
| `.dockerignore` | 构建上下文排除清单，核心目的是防止密钥泄漏 |
| `go.mod` / `go.sum` | Go 模块依赖声明（`go 1.26.4`） |
| `configs/config.yaml` | 运行时配置，随镜像 COPY 进镜像 |
| `cmd/server/main.go` | 唯一可执行入口，由 Makefile 与 Dockerfile 共同指向 |

## 3. 架构与约定

### 3.1 Makefile 目标约定

- 默认目标为 `help`，通过注释 `##` 自动生成帮助文本。
- 开发工作流：
  - `make fmt` — 格式化源码
  - `make fmt-check` — CI 风格只检不改
  - `make vet` / `make lint` — 静态检查
  - `make check` — 组合 `fmt-check + vet + go build ./...`，作为提交前自检
  - `make tidy` — 同步 `go.mod` / `go.sum`
  - `make build` — 输出 `bin/payment-server`
  - `make run` — `go run ./cmd/server -config configs/config.yaml`
  - `make test` / `make cover` — 带竞态检测的测试与覆盖率
  - `make clean` — 删除 `bin/` 与 `coverage.out`
  - `make docker-build` — `docker build -t payment-server:latest .`

### 3.2 Docker 镜像策略

- **构建阶段**：`golang:1.26-alpine`，先 `COPY go.mod go.sum` 再 `go mod download`，利用缓存复用依赖层；随后拷贝源码并静态编译到 `/out/payment-server`。
- **运行阶段**：`alpine:3.20`，仅安装 `ca-certificates`（微信支付 HTTPS 证书校验）和 `tzdata`（避免 UTC 解析失败），创建非 root 用户 `app`，暴露 `8080`。
- **健康探针**：`HEALTHCHECK` 调用 `/healthz`，注释明确“依赖抖动不应导致容器被重启”。
- **启动参数**：`ENTRYPOINT ["./payment-server"]`，`CMD ["-config", "/app/configs/config.yaml"]`。

### 3.3 安全约束（由注释与脚本共同体现）

- 商户私钥不得进入镜像层：`.dockerignore` 排除 `certs/`、`*.pem`、`*.p12`、`*.key`；`Dockerfile` 中刻意不 COPY `certs/`，改为部署时通过 K8s Secret 或 `docker -v` 挂载到 `/app/certs`。
- 本地环境变量 `.env.*` 被排除，仅保留 `.env.example`（`!.env.example`）。
- 二进制使用 `-trimpath` 去除构建机绝对路径，避免泄露本地目录结构。
- 运行镜像以非 root 用户 `app` 启动，降低容器逃逸风险。

### 3.4 版本与工具链

- Go 版本锁定在 `go 1.26.4`（见 `Dockerfile` 注释与基础镜像 `golang:1.26-alpine`）。
- 离线构建需保证基础镜像 Go 版本不低于 `go.mod` 声明值，否则构建期会联网下载 toolchain。

## 4. 观察到的约定与规则

- **工具链访问**：所有 Makefile 目标通过 `$(GO)` / `$(GOFMT)` 间接调用，禁止直接写 `go` / `gofmt`（`Makefile` 第 3–7 行注释强制说明原因）。
- **静态编译**：`CGO_ENABLED=0` 在 Makefile 顶层 export，Dockerfile 构建阶段再次显式设置，确保产物不含动态链接依赖。
- **lint 降级**：`golangci-lint` 未安装时 `make lint` 仅打印提示并退出成功，不阻断流程（`Makefile` 第 37–41 行）。
- **测试必带竞态检测**：`test` 与 `cover` 目标固定传入 `-race`（`Makefile` 第 59、62 行）。
- **构建上下文最小化**：`.dockerignore` 将 `certs/`、`.env.*`、`bin/`、`dist/`、`docs/`、`README.md`、`Makefile`、`Dockerfile` 等全部排除，注释明确指出“任何情况下都不允许进入构建上下文”（`.dockerignore` 第 21–30 行）。
- **证书挂载约定**：证书不在仓库也不在镜像中，部署时通过外部机制注入 `/app/certs`（`Dockerfile` 第 40–42 行注释）。
- **健康端点契约**：镜像 HEALTHCHECK 固定探测 `/healthz`，对应 `internal/handler/health.go` 的实现（`Dockerfile` 第 49–52 行注释）。
- **端口契约**：服务监听 `8080`，由 `EXPOSE 8080` 与 HEALTHCHECK URL 共同约定。

当前仓库未发现 CI 流水线（如 GitHub Actions、Jenkinsfile 等）文件，构建与镜像化主要通过本地 `make` 与 `docker build` 完成。