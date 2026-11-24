# mesh-server 设计

单文件静态二进制，控制面全部状态都在 `DATA_DIR`；前端构建产物用 `go:embed` 打进二进制。
HTTP 契约以 [`../CONTRACT.md`](../CONTRACT.md) 第 1 节为准，本文件只描述实现与部署。

## 1. 部署

- 部署：`k8s.mesh-server.yaml` 由 `podman kube play` 起一个 Pod，宿主机 `~/service/` 下的 justfile 统一管理；`AUTH_TOKEN` 与 `SESSION_SIGNING_KEY` 只放在 `k8s.mesh-server.secret.yaml`（不入库），play 前与主清单拼成一份：`cat k8s.mesh-server.yaml k8s.mesh-server.secret.yaml > k8s.mesh-server.deploy.yaml`；镜像在目标机本地构建（`Dockerfile.runtime` + 交叉编译好的 `linux/amd64` 二进制），不依赖镜像仓库；
- 从源码出发布镜像用 `Dockerfile`（多阶段，`ghcr.io/hm-service/mesh-server:<yyyy-MM-dd>` 与 `latest`，`linux/amd64` + `linux/arm64`）；
- 容器只跑一个二进制，根文件系统可只读，唯一可写路径是 `DATA_DIR`（默认 `/data`）；
- 应用不做 TLS，反向代理负责终止 TLS、限速与域名；应用只认 `HOST`/`X-Forwarded-Proto` 与可信反代传来的客户端地址；
- 探针：`/livez` 恒 200；`/readyz` 校验 `DATA_DIR` 权限（0700）并往 SQLite 写删 `__probe` 表，失败返回 503；`READYZ_TOKEN` 为空时不做鉴权，当前部署不设置它。

## 2. 页面

`/admin` 是唯一的控制台入口（`/` 302 过去），前端是 `frontend/` 的 Vue 3 + Vite 工程，构建产物 `frontend/dist` 被 `web.go` 嵌入。
三个页面共用一次 5 秒轮询：

| 页面 | 内容 |
| --- | --- |
| 设备列表 | 设备卡片（在线状态、配置同步状态、客户端同步状态、最后心跳），排序与「显示详情」开关，新建与详情对话框 |
| 配置 | 默认 EasyTier 模板（写回 `DATA_DIR/peer.default.toml`） |
| 客户端更新 | 按平台分启动器与客户端包两个槽位用外部地址发布，各自展示版本、摘要与地址 |

鉴权只有 `AUTH_TOKEN` 一种形态：登录页提交它，换回一个 24 小时的无状态 Bearer 凭证；没有跳过登录的开关。

## 3. 接口

| 方法 | 路径 | 认证 |
| --- | --- | --- |
| POST | `/api/auth` | 无 |
| GET | `/api/health` | 无（只回 `{ok:true}`） |
| GET | `/api/template` | Bearer |
| GET/POST | `/api/devices` | Bearer |
| GET/PUT/DELETE | `/api/devices/{id}` | Bearer |
| POST | `/api/devices/{id}/token` | Bearer |
| GET/PUT | `/api/admin/releases` | Bearer |
| DELETE | `/api/admin/releases/{platform...}`（`?slot=launcher` 只清启动器槽位） | Bearer |
| POST | `/api/register` | token 或设备签名（在请求体里） |
| POST | `/api/heartbeat` | 设备签名（在请求体里） |

请求与响应的字段、状态码、错误码见 `CONTRACT.md` 1.4；本文不重复。

## 4. 数据

`DATA_DIR` 是唯一状态源：

```
DATA_DIR/
  mesh.db              SQLite（WAL、synchronous=FULL）
  mesh.db-wal / -shm
  peer.default.toml    默认 EasyTier 模板，唯一事实来源（0600）
  server.log           JSON 行日志，4MB 高水位裁剪到 2MB（0600）
  state.json           旧版 JSON 状态库，只读保留，不再写入
```

表结构（`db/db.go` 是唯一的 SQL 位置；每张表都有与业务无关的自增主键）：

```sql
CREATE TABLE device (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id        TEXT NOT NULL UNIQUE,   -- 内部短 id d_xxxxxx，管理页与路由用
    legacy_id        TEXT NOT NULL DEFAULT '', -- 迁移时 state.json 的 id，幂等依据
    name             TEXT NOT NULL DEFAULT '',
    device_id        TEXT NOT NULL DEFAULT '', -- 客户端生成的身份，未注册为空
    hostname         TEXT NOT NULL DEFAULT '',
    pubkey           TEXT NOT NULL DEFAULT '', -- base64(ed25519 公钥)
    config_toml      TEXT NOT NULL DEFAULT '',
    config_hash      TEXT NOT NULL DEFAULT '', -- 心跳比对用；迁移数据按原样保留
    token            TEXT NOT NULL DEFAULT '', -- 一次性注册令牌，用后清空
    token_expires_at INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL DEFAULT 0,
    last_seen_at     INTEGER NOT NULL DEFAULT 0,
    last_ip          TEXT NOT NULL DEFAULT '',
    last_config_hash TEXT NOT NULL DEFAULT '', -- 最近一次心跳上报
    last_app_hash    TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX device_legacy_id ON device (legacy_id) WHERE legacy_id <> '';
CREATE UNIQUE INDEX device_device_id ON device (device_id) WHERE device_id <> '';

CREATE TABLE release (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    platform         TEXT NOT NULL UNIQUE,        -- GOOS/GOARCH
    version          TEXT NOT NULL DEFAULT '',    -- 客户端包版本号
    url              TEXT NOT NULL DEFAULT '',
    hash             TEXT NOT NULL DEFAULT '',    -- 客户端包 16 位摘要，心跳比对
    sha256           TEXT NOT NULL DEFAULT '',    -- 客户端包完整摘要，客户端校验下载
    notes            TEXT NOT NULL DEFAULT '',
    launcher_version TEXT NOT NULL DEFAULT '',    -- 启动器版本号
    launcher_url     TEXT NOT NULL DEFAULT '',
    launcher_sha256  TEXT NOT NULL DEFAULT ''     -- 启动器完整摘要，客户端自己比对
);

CREATE TABLE setting (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    app_version   TEXT NOT NULL DEFAULT '',
    app_url       TEXT NOT NULL DEFAULT '',
    app_hash      TEXT NOT NULL DEFAULT '', -- 16 位，客户端比对
    app_sha256    TEXT NOT NULL DEFAULT '', -- 完整摘要，客户端校验下载
    app_notes     TEXT NOT NULL DEFAULT ''
);
```

`release` 一行一个平台，两个槽位（启动器、客户端包）的列各自独立写入：发布或清空一个槽位不改动另一个。两个槽位都没有内容时不留行，所以 `releases` 里出现的平台至少发布了一个工件。

`setting` 只有一行，仅用于把旧版单条设置迁进 `release`：启动时在 `release` 为空的情况下读取一次，之后不再写入。

## 5. 旧数据迁移

`app/migrate.go` 在每次启动时读 `DATA_DIR/state.json`（只读，不删除、不改写），把设备与设置导入 SQLite：

- 设备以 `legacy_id = state.json 的 id` 写入，配合唯一索引与 `INSERT ... ON CONFLICT DO NOTHING`，重复启动不会重复插入；
- 已存在的行一律跳过，所以迁移之后再在管理页改过的设备不会被旧文件覆盖；
- `config_hash` 原样保留（历史上的值可能是 16 位，也可能是 64 位完整摘要），不做重新计算；
- 旧的单条设置（`state.json` 的 `settings` 或旧 `setting` 表）只在 `release` 为空时导入 `windows/amd64` 一行；
- `state.json` 本身保留在原处，作为回滚材料。

## 6. 安全模型

| 面 | 机制 |
| --- | --- |
| 管理面 | `AUTH_TOKEN` 的 Argon2id（m=19456、t=2、p=1）哈希 + 无状态 Bearer（HMAC-SHA256、24h、`SESSION_SIGNING_KEY`）；登录响应有 1s + rand(0..200ms) 的下限时长 |
| 设备面 | Ed25519 请求签名（`MESH1` 五行原文）+ 300 秒时间窗 + nonce 重放缓存 |
| 重新注册 | 已绑定设备重新注册必须出示签名或有效令牌，只有 `device_id` + 公钥不足以取回含 `network_secret` 的配置 |
| 发布包 | 客户端包：`app_hash`（16 位）用于判定，`app_sha256`（完整）用于下载后校验；启动器：只有 `launcher_sha256`（完整），由客户端自己比对 |
| 客户端地址 | 只有 `TRUSTED_PROXIES` 内的对端才能用 `X-Forwarded-For` / `X-Real-IP`，默认不信任任何来源 |
| 输入 | JSON 请求体 1MiB 上限，发布的更新包 200MB 上限，设备数 200 上限，`platform` 的 GOOS/GOARCH 白名单 |
| 响应头 | `X-Content-Type-Options`、`Referrer-Policy`、`X-Frame-Options`、严格 `Content-Security-Policy` |
| 并发 | `/api/auth`、`/api/*`、静态资源、`/readyz` 各自独立信号量；Argon2id 并发上限 4 |

## 7. 环境变量与 Secrets

| 变量 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- |
| `TZ` | 否 | Asia/Shanghai | 日志与时间格式化时区，非法值拒绝启动 |
| `HOST` | 否 | 0.0.0.0 | 与 `PORT` 一起决定监听地址 |
| `PORT` | 否 | `:8088` | 与 `HOST` 一起决定监听地址；未设置时用 `:8088` |
| `DATA_DIR` | 否 | `/data` | 唯一状态目录，`-data` 参数优先级更高 |
| `TRUSTED_PROXIES` | 否 | 空（不信任） | 逗号分隔的 IP 或 CIDR |
| `MAX_AUTH_CONCURRENCY` | 否 | 32 | `/api/auth` 并发上限 |
| `MAX_API_CONCURRENCY` | 否 | 128 | `/api/*` 并发上限 |
| `MAX_STATIC_CONCURRENCY` | 否 | 64 | 静态资源并发上限 |
| `AUTH_TOKEN` | 是 | — | 管理台唯一凭证，`^[A-Za-z0-9_]{16,128}$`；启动时哈希，明文读完即从环境表移除 |
| `SESSION_SIGNING_KEY` | 否 | 随机 | Bearer 的 HMAC 密钥，`^[A-Za-z0-9_]{32,128}$`；轮换它等于吊销全部已发凭证 |
| `READYZ_TOKEN` | 否 | 空 | 非空时 `/readyz` 必须带同值 token；读入后立即从环境表移除 |
| `GOMEMLIMIT` | 否 | 128MiB | 未设置时由程序设置该软上限 |

数值型变量解析失败或越界时直接拒绝启动，退出码 1。

## 8. 日志与生命周期

- 日志是 JSON 行，同时写 stdout 与 `DATA_DIR/server.log`；每条访问日志带 `client_ip`、`method`、`path`、`status`、`duration_ms`、`timestamp`；`/livez` 不记；
- 启动成功后打印 `server started`，停止时依次打印 `shutdown signal received`、`server shutdown`；
- 收到 SIGINT/SIGTERM 后用 5 秒上限优雅关闭：超时退出码 254，关闭数据库或日志失败退出码 255，正常 0。