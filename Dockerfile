# syntax=docker/dockerfile:1
#
# 支付服务镜像：多阶段构建，编译产物为静态二进制，运行阶段不含任何构建工具链。
#
# 注意 go.mod 的 go 指令为 1.26.4：若基础镜像的 Go 版本低于它，
# 构建时会自动下载匹配的 toolchain（需要构建期联网）。
# 离线构建请把基础镜像换成版本不低于 go.mod 声明的 golang:1.26.x-alpine。

# ---------- 构建阶段 ----------
FROM golang:1.26-alpine AS builder

WORKDIR /src

# 先只拷贝依赖清单再下载，让源码改动不会击穿这一层缓存：
# 依赖下载是整个构建里最慢的一步，能复用就尽量复用
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 产出静态二进制，运行阶段才敢用不含 glibc 的 alpine
# -trimpath 去掉编译机的绝对路径，避免把本地目录结构泄漏进产物
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" \
	-o /out/payment-server ./cmd/server

# ---------- 运行阶段 ----------
FROM alpine:3.20

# ca-certificates：将来调用微信支付 HTTPS 接口时校验服务端证书所必需
# tzdata：alpine 默认不带时区数据库，缺失会让日志与订单时间戳退化成 UTC 解析失败
RUN apk add --no-cache ca-certificates tzdata \
	&& addgroup -S app \
	&& adduser -S -G app app

WORKDIR /app

COPY --from=builder /out/payment-server ./payment-server
COPY configs/config.yaml ./configs/config.yaml

# 刻意不 COPY certs/：商户私钥绝不能进镜像层，
# 镜像一旦被推到仓库，历史层里的私钥就永久泄漏了。
# 证书由部署时通过 K8s Secret 或 docker -v 挂载到 /app/certs。

# 非 root 运行：容器逃逸时把攻击者拿到的权限降到最低
USER app

EXPOSE 8080

# 探针打到 /healthz（存活），不查依赖：
# 依赖抖动不应该导致容器被重启，详见 internal/handler/health.go 的说明
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
	CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["./payment-server"]
CMD ["-config", "/app/configs/config.yaml"]
