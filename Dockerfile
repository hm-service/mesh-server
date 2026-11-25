# ==========================================
# 第一阶段：构建前端
# ==========================================
FROM --platform=$BUILDPLATFORM node:26.13.1-alpine AS frontend

RUN npm install -g pnpm@12.23.0

WORKDIR /build/frontend

COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

COPY frontend/ ./
RUN pnpm build

# ==========================================
# 第二阶段：构建 Go 二进制
# ==========================================
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

# CGO_ENABLED=0：纯静态链接，运行时不需要 libc
ENV CGO_ENABLED=0

# Go 模块代理：默认走国内镜像（海外构建可用 --build-arg GOPROXY=https://proxy.golang.org,direct 覆盖）
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /build

COPY go.* ./
RUN go mod download

COPY . .
COPY --from=frontend /build/frontend/dist ./frontend/dist

# -ldflags="-w -s"：移除调试信息与符号表；-trimpath 去掉构建机路径
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-w -s" -trimpath -o mesh-server .

# ==========================================
# 第三阶段：取 easytier-cli
# ==========================================
# 控制面靠它调种子的 management RPC（签发与撤销凭据）。它必须和种子上
# easytier-core 的次版本一致，升级 core 时同步改 EASYTIER_VERSION。
# release 的资产名用 x86_64/aarch64，和 Docker 的 TARGETARCH 不同名，这里映射一次。
FROM alpine:3 AS easytier

ARG TARGETARCH
ARG EASYTIER_VERSION=v2.7.1

RUN set -eux; \
    case "$TARGETARCH" in \
        amd64) arch=x86_64 ;; \
        arm64) arch=aarch64 ;; \
        *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    apk --no-cache add curl unzip; \
    curl -fsSL -o /tmp/et.zip "https://github.com/EasyTier/EasyTier/releases/download/${EASYTIER_VERSION}/easytier-linux-${arch}-${EASYTIER_VERSION}.zip"; \
    unzip -j /tmp/et.zip "easytier-linux-${arch}/easytier-cli" -d /out; \
    chmod 0755 /out/easytier-cli; \
    rm -f /tmp/et.zip

# ==========================================
# 第四阶段：运行
# ==========================================
FROM alpine:3

# Podman 会把 kube 的 httpGet 探针翻译成 curl -f <url> || exit 1 在容器里执行，
# 镜像里没有 curl 探针就恒失败。
RUN apk --no-cache add ca-certificates curl tzdata

WORKDIR /app

# 前端产物已经 go:embed 进去，frontend/ 整个不进最终镜像
COPY --from=builder /build/mesh-server .
COPY --from=easytier /out/easytier-cli /usr/local/bin/easytier-cli

VOLUME ["/data"]
EXPOSE 8080

CMD ["./mesh-server"]
