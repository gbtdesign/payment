---
kind: build_system
name: Go 支付服务构建与镜像打包体系（Makefile + 多阶段 Dockerfile）
category: build_system
scope:
    - '**'
source_files:
    - Makefile
    - Dockerfile
    - .dockerignore
    - go.mod
    - go.sum
    - cmd/server/main.go
    - configs/config.yaml
---

## 1. 使用的系统与工具

- **语言/工具链**：Go（`go.mod` 声明 `go 1.26.4`），通过 Makefile 统一调用，避免直接写裸 `go` / `gofmt`。
- **构建入口**：`Makefile`，提供 `help`、`fmt`、`fmt-check`、`vet`、`lint`、`check`、`tidy`、`build`、`run`、`test`、`cover`、`clean`、`docker-build` 等目标。
- **容器化**：`Dockerfile` 使用官方 `golang:1.26-alpine` 作为 builder，最终镜像基于 `alpine:3.20`。
- **静态编译**：`CGO_ENABLED=0`，产物不依赖 glibc，可放入 alpine/scratch。
- **可选 Lint**：`golangci-lint` 未安装时跳过而不报错（`Makefile` 第 37-41 行用 `command -v` 判断）。
- **无 CI 流水线文件**：仓库根未发现 `.github/workflows`、`.gitlab-ci.yml`、`Jenkinsfile` 等 CI 配置；CI 规则由 Makefile 的 `check` 目标约定（见下文“约束”）。

## 2. 关键文件

| 文件 | 作用 |
|---|---|
| `Makefile` | 全部本地构建、格式化、检查、测试、覆盖率、容器构建的统一入口 |
| `Dockerfile` | 多阶段构建：builder 阶段编译静态二进制，运行阶段仅含运行时依赖 |
| `.dockerignore` | 排除证书、密钥、构建产物、文档，防止敏感信息进入镜像层 |
| `go.mod` / `go.sum` | Go 模块依赖清单（被 Dockerfile 优先 COPY 以复用缓存层） |
| `cmd/server/main.go` | 唯一可执行入口，Makefile 中 `MAIN := ./cmd/server` |
| `configs/config.yaml` | 默认配置文件，随镜像一起 COPY |
| `certs/README.md` | 证书说明（证书不进镜像，详见 Dockerfile 注释） |

## 3. 架构与约定

### 3.1 Makefile 工具发现策略

```makefile
GO        := $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOFMT     := $(shell command -v gofmt 2>/dev/null || echo /usr/local/go/bin/gofmt)
```

- 优先从 PATH 查找（适配 CI、容器环境）；回退到 `/usr/local/go/bin/go`（适配本机未加入 PATH 的场景）。
- 所有子目标统一通过 `$(GO)` / `$(GOFMT)` 调用，禁止在目标体里直接写裸 `go` / `gofmt`（Makefile 顶部注释明确说明原因）。

### 3.2 构建产物与路径

- 二进制输出目录：`bin/payment-server`（`BUILD_DIR := bin`，`BINARY := payment-server`）。
- 编译参数：`-trimpath -ldflags "-s -w"`，去除绝对路径并剥离符号表。
- 默认目标：`help`（通过 `grep` 解析 `##` 注释生成帮助文本）。

### 3.3 Docker 多阶段构建

- **Builder 阶段**：`FROM golang:1.26-alpine AS builder`，先 `COPY go.mod go.sum` 再 `go mod download`，利用 Docker 层缓存加速依赖下载。
- **运行阶段**：`FROM alpine:3.20`，仅安装 `ca-certificates`（HTTPS 校验）和 `tzdata`（时区数据库），创建非 root 用户 `app`，`USER app` 运行。
- 证书目录 `certs/` 刻意不 COPY，部署时通过 K8s Secret 或 `-v` 挂载到 `/app/certs`。
- HEALTHCHECK 探测 `/healthz`，间隔 30s、超时 3s、启动宽限 5s、重试 3 次。

### 3.4 .dockerignore 安全策略

- 显式排除 `certs/`、`*.pem`、`*.p12`、`*.key`、`.env`、`.env.*`（保留 `!.env.example`）。
- 排除 `.git`、`bin/`、`dist/`、`coverage.out`、`docs/`、`Makefile`、`Dockerfile` 等构建期/文档文件。
- 注释强调：“镜像层一旦推送到仓库就无法从历史中抹掉”，因此证书不能进上下文。

### 3.5 测试与覆盖率

- `make test`：`go test ./... -race`，启用竞态检测。
- `make cover`：生成 `coverage.out`，并通过 `go tool cover -func` 打印函数级覆盖率摘要。

## 4. 观察到的约定与约束

- **静态编译强制**：Makefile 顶层 `export CGO_ENABLED := 0`，Dockerfile 也再次设置 `CGO_ENABLED=0`，确保产物不含 glibc 依赖。
- **Go 版本锁定**：`go.mod` 声明 `go 1.26.4`；Dockerfile 使用匹配的 `golang:1.26-alpine`。注释指出离线构建需保证基础镜像版本不低于该值，否则构建期会联网拉取 toolchain。
- **二进制命名与位置固定**：始终输出为 `bin/payment-server`，由 `ENTRYPOINT ["./payment-server"]` 直接拉起。
- **非 root 运行**：容器内创建 `adduser -S -G app app` 并以 `USER app` 运行，降低容器逃逸风险。
- **证书外置**：`certs/` 被 `.dockerignore` 排除且 Dockerfile 不 COPY，部署侧负责注入（K8s Secret 或 volume mount）。
- **提交前自检约定**：`make check` 依次执行 `fmt-check`、`vet`、`go build ./...`，是仓库约定的本地预检流程（Makefile 注释称其为“提交前自检”）。
- **Lint 可选**：`make lint` 仅在 `golangci-lint` 已安装时执行，未安装则打印提示后跳过，不会导致失败。
- **端口暴露**：`EXPOSE 8080`，HEALTHCHECK 探测 `http://127.0.0.1:8080/healthz`。
- **默认配置路径**：`run` 目标传入 `-config configs/config.yaml`，镜像中也 COPY 了该文件，作为运行时默认配置。

## 5. 缺失项

- 未发现 CI 流水线（GitHub Actions / GitLab CI / Jenkins 等）；发布与持续集成规则未在仓库中体现。
- 未发现独立的 `build.sh` / `release.sh` 脚本；所有构建逻辑集中在 `Makefile`。
- 未发现版本化标签策略（如 `IMAGE := payment-server:$(VERSION)`），`docker-build` 目标固定打 `:latest` 标签。