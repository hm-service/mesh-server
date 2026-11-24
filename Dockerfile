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
# 第三阶段：运行
# ==========================================
FROM alpine:3

# Podman 会把 kube 的 httpGet 探针翻译成 curl -f <url> || exit 1 在容器里执行，
# 镜像里没有 curl 探针就恒失败。
RUN apk --no-cache add ca-certificates curl tzdata

WORKDIR /app

# 运行时只有这一个二进制：前端产物已经 go:embed 进去，frontend/ 整个不进最终镜像
COPY --from=builder /build/mesh-server .

VOLUME ["/data"]
EXPOSE 8088

CMD ["./mesh-server"]
