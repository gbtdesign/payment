---
kind: dependency_management
name: Go 模块依赖管理（go.mod + go.sum + Makefile tidy）
category: dependency_management
scope:
    - '**'
source_files:
    - go.mod
    - go.sum
    - Makefile
---

## 1. 使用的系统/方案

本项目采用 Go 官方模块系统，通过 `go.mod` 声明直接依赖、`go.sum` 锁定间接依赖版本，并通过 `Makefile` 中的 `tidy` 目标统一维护依赖清单。未使用 vendor 目录、私有代理或 GOPRIVATE 等机制。

## 2. 关键文件

- `go.mod`：模块名为 `payment`，Go 版本固定为 `1.26.4`，直接依赖仅 4 个库：
  - `github.com/gin-gonic/gin v1.10.0`（HTTP 框架）
  - `github.com/go-playground/validator/v10 v10.20.0`（参数校验）
  - `github.com/spf13/viper v1.21.0`（配置加载）
  - `go.uber.org/zap v1.28.0`（结构化日志）
  其余约 35 个包以 `// indirect` 标记为传递依赖。
- `go.sum`：与 `go.mod` 配套，提供每个依赖的哈希校验。
- `Makefile`：`tidy` 目标调用 `go mod tidy`，注释明确说明“会同步修改 go.mod / go.sum”。构建目标使用 `CGO_ENABLED := 0` 进行静态编译，避免运行时依赖宿主 glibc。

## 3. 架构与约定

- **单一模块**：仓库根即唯一 Go module，所有业务代码位于 `internal/` 下，无多模块拆分。
- **依赖声明方式**：直接依赖集中在 `go.mod` 的 `require` 块中；间接依赖由工具链自动填充，开发者不应手动编辑。
- **版本锁定**：通过 `go.sum` 保证可重复构建；`go.mod` 中所有依赖均带精确版本号（含 major version 路径如 `/v10`、`/v2`），未见 `replace` 指令指向本地替换或私有仓库。
- **更新流程**：新增/删除 import 后执行 `make tidy`，该命令会调用 `go mod tidy` 整理依赖并同步 `go.mod` 与 `go.sum`。
- **构建隔离**：`Makefile` 设置 `CGO_ENABLED := 0`，配合 Dockerfile 生成不依赖宿主 C 库的二进制，使依赖在容器内完全自包含。

## 4. 约定与约束

- **依赖入口**：新增第三方库应通过 `go get` 引入并在 `go.mod` 中声明，随后运行 `make tidy` 清理间接依赖（依据 `Makefile` 中 `tidy` 目标的注释：“整理依赖（增删 import 后执行，会同步修改 go.mod / go.sum）”）。
- **Go 版本约束**：`go.mod` 顶部声明 `go 1.26.4`，所有构建与测试必须在该版本或兼容版本上执行（由 `go mod` 工具链强制）。
- **无私有仓库/代理配置**：仓库内未发现 `.env`、`.gitconfig`、`GOPROXY`、`GOPRIVATE`、`GONOSUMDB` 等环境变量或配置文件，也未见任何 `replace` 指令指向私有源；依赖全部来自公共 Go Proxy（默认 `https://proxy.golang.org`）。
- **无 vendoring**：仓库根不存在 `vendor/` 目录，也不存在 `-mod=vendor` 构建标志，依赖解析走网络缓存而非本地副本。
- **静态编译约束**：`Makefile` 导出 `CGO_ENABLED := 0`，因此依赖不得引入需要 CGO 的 C 扩展库（否则构建失败）。