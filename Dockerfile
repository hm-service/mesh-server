# ==========================================
# 第一阶段：构建前端
# ==========================================
# 基础镜像钉 manifest list digest：tag 会随上游重发漂移，digest 不会。升级时先
# `podman pull node:26.13.1-alpine`，再用
# `podman image inspect --format '{{index .RepoDigests 0}}'` 取新 digest 替换这里。
FROM --platform=$BUILDPLATFORM node:26.13.1-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5 AS frontend

RUN npm install -g pnpm@12.23.0

WORKDIR /build/frontend

COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

COPY frontend/ ./
RUN pnpm build

# ==========================================
# 第二阶段：构建 Go 二进制
# ==========================================
# 同样钉 digest；tag 里的 alpine 变体会随上游重发漂移。
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:0fd028db9bdd4f01657bc8047c209b04d8ce19b853d776bd6f1e76a41729b936 AS builder

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
#
# apk 包与基础镜像一起钉版本：v3.24 仓库里的包版本会继续前进，写死才能保证
# 不同时间、不同架构的构建拿到同一组包。升级基础镜像 digest 后，用
# `podman run --rm <image> apk policy <pkg>` 取新版本一并替换；apk add 失败即
# 说明写死的版本已从仓库移除。
FROM alpine:3.24.4@sha256:0f49f331d9212aff11b932863be4eb01cdf962969aa86dab7d1ee21ac6f5f52b AS easytier

ARG TARGETARCH
ARG EASYTIER_VERSION=v2.7.1

# 校验和取自上游同一 release 的 SHA256SUMS：
# https://github.com/EasyTier/EasyTier/releases/download/v2.7.1/SHA256SUMS
# 升级 EASYTIER_VERSION 时必须同时替换这两个值（从新 release 的 SHA256SUMS 取），
# 否则构建会在 sha256sum -c 处失败。
ARG EASYTIER_SHA256_AMD64=8dc4f8576c7a4978c1f000630014b75ec88d172f6cb10935e72092ddd5ec7a9a
ARG EASYTIER_SHA256_ARM64=7f4d78aefe7437d7f213089ac7184988665e9c4d1ff163b8ad08637c1c88bb0a

RUN set -eux; \
    case "$TARGETARCH" in \
        amd64) arch=x86_64; want="$EASYTIER_SHA256_AMD64" ;; \
        arm64) arch=aarch64; want="$EASYTIER_SHA256_ARM64" ;; \
        *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    apk --no-cache add curl=8.23.0-r0 unzip=6.0-r16; \
    curl -fsSL -o /tmp/et.zip "https://github.com/EasyTier/EasyTier/releases/download/${EASYTIER_VERSION}/easytier-linux-${arch}-${EASYTIER_VERSION}.zip"; \
    echo "$want  /tmp/et.zip" | sha256sum -c -; \
    unzip -j /tmp/et.zip "easytier-linux-${arch}/easytier-cli" -d /out; \
    chmod 0755 /out/easytier-cli; \
    rm -f /tmp/et.zip

# ==========================================
# 第四阶段：运行
# ==========================================
FROM alpine:3.24.4@sha256:0f49f331d9212aff11b932863be4eb01cdf962969aa86dab7d1ee21ac6f5f52b AS runtime

# Podman 会把 kube 的 httpGet 探针翻译成 curl -f <url> || exit 1 在容器里执行，
# 镜像里没有 curl 探针就恒失败。
# 包版本钉死，升级方式见第三阶段的注释。
RUN apk --no-cache add ca-certificates=20261020-r0 curl=8.23.0-r0 tzdata=2026f-r0

WORKDIR /app

# 前端产物已经 go:embed 进去，frontend/ 整个不进最终镜像
COPY --from=builder /build/mesh-server .
COPY --from=easytier /out/easytier-cli /usr/local/bin/easytier-cli

VOLUME ["/data"]
EXPOSE 8080

CMD ["./mesh-server"]
