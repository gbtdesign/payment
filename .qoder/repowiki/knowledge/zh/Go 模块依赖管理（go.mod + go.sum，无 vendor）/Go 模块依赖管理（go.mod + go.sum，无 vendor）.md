---
kind: dependency_management
name: Go 模块依赖管理（go.mod + go.sum，无 vendor）
category: dependency_management
scope:
    - '**'
source_files:
    - go.mod
    - go.sum
    - Makefile
---

## 1. 使用的系统/方法

仓库使用 Go Modules 作为唯一的第三方依赖管理机制：
- 模块声明与版本锁定通过 `go.mod` / `go.sum` 完成。
- 构建、格式化、静态检查、测试等统一由根目录的 `Makefile` 驱动，所有目标均通过 `$(GO)` / `$(GOFMT)` 变量调用工具链，避免直接裸写 `go` / `gofmt`（见 Makefile 第 3–8 行注释与赋值）。
- 未使用 `vendor/` 目录；依赖从远程代理拉取后缓存到本地模块缓存。
- 未发现私有仓库或自定义代理配置（仓库内无 `GOPRIVATE`、`GOPROXY`、`GONOSUMCHECK` 环境变量设置，也未在 `.env.example` 中暴露相关变量）。

## 2. 关键文件

- `go.mod`：模块名为 `payment`，Go 版本固定为 `1.26.4`。直接依赖仅 4 个：`github.com/gin-gonic/gin v1.10.0`、`github.com/go-playground/validator/v10 v10.20.0`、`github.com/spf13/viper v1.21.0`、`go.uber.org/zap v1.28.0`。其余均为 indirect 传递依赖。
- `go.sum`：对应 `go.mod` 的校验和清单（随 `go mod tidy` 同步更新）。
- `Makefile`：提供 `tidy` 目标（`go mod tidy`），是维护依赖声明的唯一入口；同时提供 `build`（`-trimpath -ldflags "-s -w"`）、`test`（`-race`）、`docker-build` 等工程化目标。

## 3. 架构与约定

- **依赖声明**：所有第三方库以显式 `require` 条目写入 `go.mod`，间接依赖由 `go mod tidy` 自动补充并标记 `// indirect`。
- **版本策略**：当前全部依赖采用精确版本号（非 `replace` 或 `pseudo-version`），包括 gin、viper、zap、validator 等主依赖以及 golang.org/x/* 系列标准扩展库。
- **构建隔离**：Makefile 导出 `CGO_ENABLED := 0`，确保产物不依赖宿主 glibc，可放入 alpine/scratch 镜像运行。
- **二进制优化**：`make build` 使用 `-trimpath -ldflags "-s -w"` 去除路径与调试信息。
- **CI/容器友好**：Makefile 通过 `command -v go` 优先查找 PATH 中的 go，回退到 `/usr/local/go/bin/go`，适配本机未配置 PATH 的环境。

## 4. 约定与约束

- **依赖整理必须通过 `make tidy`**：Makefile 中 `tidy` 目标的注释明确说明“增删 import 后执行，会同步修改 go.mod / go.sum”，这是仓库内对依赖变更流程的文档化约定。
- **禁止裸调 `go` / `gofmt`**：Makefile 开头注释强制要求“所有目标统一通过 $(GO) / $(GOFMT) 调用工具链，而不是直接写 go / gofmt”，否则在本机（Go 安装在 `/usr/local/go/bin` 且未加入 PATH）会报 `command not found`。
- **未启用 vendor 模式**：仓库根不存在 `vendor/` 目录，也未在 `go.mod` 中使用 `go 1.x` 的 `use ./vendor` 语义，依赖始终从远端解析。
- **未配置私有代理**：仓库内未出现 `GOPRIVATE`、`GOPROXY`、`GONOSUMCHECK` 等 Go 代理相关配置，默认使用官方 `proxy.golang.org`。
- **lint 可选**：`make lint` 在未安装 `golangci-lint` 时跳过而不报错，属于软约束而非硬性阻断。
- **测试带竞态检测**：`make test` / `make cover` 统一加 `-race` 标志，作为代码质量基线。

## 5. 观察到的依赖范围

直接依赖仅 4 个核心库（gin HTTP 框架、validator 参数校验、viper 配置加载、zap 结构化日志），其余均为这些库的传递依赖，未见业务自研的私有 Go 包引用。