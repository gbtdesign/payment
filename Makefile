# 支付服务构建脚本
#
# 所有目标统一通过 $(GO) / $(GOFMT) 调用工具链，而不是直接写 go / gofmt：
# 本机 Go 安装在 /usr/local/go/bin 且未加入 PATH，裸写 go 会 command not found。
# command -v 优先，保证在已配置好 PATH 的环境（CI、容器）里用的是同一套工具链。

GO        := $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
GOFMT     := $(shell command -v gofmt 2>/dev/null || echo /usr/local/go/bin/gofmt)

MAIN      := ./cmd/server
BUILD_DIR := bin
BINARY    := payment-server
CONFIG    := configs/config.yaml
IMAGE     := payment-server

# 静态编译：产物不依赖 glibc，运行阶段可以放在 alpine 甚至 scratch 里
export CGO_ENABLED := 0

.DEFAULT_GOAL := help

.PHONY: help fmt fmt-check vet lint check tidy build run test cover clean docker-build

help: ## 列出全部可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "} {printf "  %-13s %s\n", $$1, $$2}'

fmt: ## 格式化全部 Go 源码
	$(GOFMT) -w -s .

fmt-check: ## 只检查格式，不修改文件（有输出即代表未格式化）
	$(GOFMT) -l -s .

vet: ## go vet 静态检查
	$(GO) vet ./...

lint: ## golangci-lint 检查，未安装时跳过而不报错
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint 未安装，已跳过；安装方式见 README 的「工程化」一节"; \
	fi

check: fmt-check vet ## 提交前自检：格式 + 静态检查 + 编译
	$(GO) build ./...
	@echo "check 通过"

tidy: ## 整理依赖（增删 import 后执行，会同步修改 go.mod / go.sum）
	$(GO) mod tidy

build: ## 编译服务端二进制到 bin/
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -ldflags "-s -w" -o $(BUILD_DIR)/$(BINARY) $(MAIN)
	@echo "已生成 $(BUILD_DIR)/$(BINARY)"

run: ## 本地启动服务（前台运行，Ctrl-C 触发优雅关闭）
	$(GO) run $(MAIN) -config $(CONFIG)

test: ## 运行全部测试，带竞态检测
	$(GO) test ./... -race

cover: ## 运行测试并输出覆盖率报告到 coverage.out
	$(GO) test ./... -race -coverprofile=coverage.out
	$(GO) tool cover -func=coverage.out | tail -1

clean: ## 清理构建产物与覆盖率文件
	rm -rf $(BUILD_DIR) coverage.out

docker-build: ## 构建容器镜像
	docker build -t $(IMAGE):latest .
