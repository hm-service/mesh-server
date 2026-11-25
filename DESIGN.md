# mesh-server 设计

单文件静态二进制，控制面全部状态都在 `DATA_DIR`；前端构建产物用 `go:embed` 打进二进制。
HTTP 契约以 [`../CONTRACT.md`](../CONTRACT.md) 第 1 节为准，本文件只描述实现与部署。

## 1. 部署

- 部署：`k8s.mesh-server.deploy.yaml` 由 `podman kube play` 起一个 Pod，宿主机 `~/service/` 下的 justfile 统一管理；`AUTH_TOKEN`、`SESSION_SIGNING_KEY`、`READYZ_TOKEN` 只放在 `k8s.mesh-server.secret.yaml`（不入库），主清单里的探针令牌写成占位符 `__READYZ_TOKEN__`，部署前渲染：

  ```sh
  R=$(sed -n 's/^ *READYZ_TOKEN: "\([^"]*\)".*/\1/p' k8s.mesh-server.secret.yaml)
  { cat k8s.mesh-server.yaml k8s.mesh-server.secret.yaml; } | sed "s/__READYZ_TOKEN__/$R/" > k8s.mesh-server.deploy.yaml
  ```

  镜像由 CI（GitHub Actions）构建并推到 `ghcr.io/hm-service/mesh-server`，部署机只拉取，清单用 `imagePullPolicy: Always`；
- 从源码出镜像用 `Dockerfile`（多阶段，`ghcr.io/hm-service/mesh-server:<yyyy-MM-dd>` 与 `latest`，`linux/amd64` + `linux/arm64`；最后一个阶段带进 `easytier-cli`，见第 6 节）；
- `Dockerfile.runtime` 是离线兜底：把交叉编译好的 `linux/amd64` 二进制放在同目录，在目标机本地出镜像，不依赖镜像仓库。它没有 `easytier-cli`，用它时 `ET_CLI` 留空，或者自己把 CLI 打进镜像；
- 容器只跑一个二进制，根文件系统可只读，唯一可写路径是 `DATA_DIR`（默认 `/data`）；
- 应用不做 TLS，反向代理负责终止 TLS、限速与域名；应用只认 `HOST`/`X-Forwarded-Proto` 与可信反代传来的客户端地址；
- 探针：`/livez` 恒 200；`/readyz` 校验 `DATA_DIR` 权限（0700）并往 SQLite 写删 `__probe` 表，失败返回 503；`READYZ_TOKEN` 为空时不做鉴权，但探针会写 `__probe` 表，部署里必须设置，否则谁都能触发写入。

## 2. 页面

`/admin` 是唯一的控制台入口（`/` 302 过去），前端是 `frontend/` 的 Vue 3 + Vite 工程，构建产物 `frontend/dist` 被 `web.go` 嵌入。设计变量集中在 `frontend/src/style.css`，不引入组件库。
设备页每 5 秒轮询一次；模板与更新页在进入时加载。

| 页面 | 内容 |
| --- | --- |
| 设备 | 按分组排的表：名称与主机名、mesh 地址、在线状态、配置是否同步、客户端版本、最后在线；行内拖拽排序与改分组。详情抽屉里编辑配置（含「重置为模板」）、轮换凭据、签发注册令牌、撤销设备 |
| 默认模板 | 当前生效的默认 EasyTier 模板（`DATA_DIR/peer.default.toml`，缺失或空白时展示内置模板）；保存写回该文件 |
| 客户端更新 | 按平台分启动器与客户端包两个槽位用外部地址发布，各自展示版本、摘要与地址 |

鉴权只有 `AUTH_TOKEN` 一种形态：登录页提交它，换回一个 24 小时的无状态 Bearer 凭证；没有跳过登录的开关。

## 3. 接口

`/api/` 下只有三个入口：`/api/auth`（特例）、`/api/v1/`（设备公开）、`/api/admin/`（管理面）。

| 方法 | 路径 | 认证 |
| --- | --- | --- |
| POST | `/api/auth` | 无 |
| POST | `/api/v1/register` | token 或设备签名（在请求体里） |
| POST | `/api/v1/heartbeat` | 设备签名（在请求体里） |
| GET | `/api/admin/template` | Bearer |
| GET/POST | `/api/admin/devices` | Bearer |
| GET/PUT/DELETE | `/api/admin/devices/{id}` | Bearer |
| POST | `/api/admin/devices/{id}/token` | Bearer |
| POST | `/api/admin/devices/{id}/credential` | Bearer |
| DELETE | `/api/admin/devices/{id}/credential` | Bearer |
| PUT | `/api/admin/devices/{id}/group` | Bearer |
| PUT | `/api/admin/layout` | Bearer |
| GET/POST | `/api/admin/groups` | Bearer |
| PUT/DELETE | `/api/admin/groups/{id}` | Bearer |
| GET/PUT | `/api/admin/releases` | Bearer |
| DELETE | `/api/admin/releases/{platform...}`（`?slot=launcher` 只清启动器槽位） | Bearer |

请求与响应的字段、状态码、错误码见 `CONTRACT.md` 1.4；本文不重复。

## 4. 数据

`DATA_DIR` 是唯一状态源：

```
DATA_DIR/
  mesh.db              SQLite（WAL、synchronous=FULL）
  mesh.db-wal / -shm
  peer.default.toml    默认 EasyTier 模板，唯一事实来源（0600）
  server.log           JSON 行日志，4MB 高水位裁剪到 2MB（0600）
```

其它文件（例如旧版本留下的 `state.json`）既不读也不写。

表结构（`db/db.go` 是唯一的 SQL 位置；每张表都有与业务无关的自增主键）：

```sql
CREATE TABLE device (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    public_id        TEXT NOT NULL UNIQUE,   -- 内部短 id d_xxxxxx，管理页与路由用
    name             TEXT NOT NULL DEFAULT '',
    device_id        TEXT NOT NULL DEFAULT '', -- 客户端生成的身份，未注册为空
    hostname         TEXT NOT NULL DEFAULT '',
    pubkey           TEXT NOT NULL DEFAULT '', -- base64(ed25519 公钥)
    config_toml      TEXT NOT NULL DEFAULT '',
    config_hash      TEXT NOT NULL DEFAULT '', -- 心跳比对用
    token            TEXT NOT NULL DEFAULT '', -- 一次性注册令牌，用后清空
    token_expires_at INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL DEFAULT 0,
    last_seen_at     INTEGER NOT NULL DEFAULT 0,
    last_ip          TEXT NOT NULL DEFAULT '',
    last_config_hash TEXT NOT NULL DEFAULT '', -- 最近一次心跳上报
    last_app_hash    TEXT NOT NULL DEFAULT '',
    last_platform    TEXT NOT NULL DEFAULT '', -- 最近一次心跳上报的 GOOS/GOARCH
    group_id         INTEGER NOT NULL DEFAULT 0, -- 0 表示未分组
    position         INTEGER NOT NULL DEFAULT 0  -- 组内排序，0 起
);
CREATE UNIQUE INDEX device_device_id ON device (device_id) WHERE device_id <> '';

CREATE TABLE device_group (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    name     TEXT NOT NULL DEFAULT '',
    position INTEGER NOT NULL DEFAULT 0
);

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

CREATE TABLE credential (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    credential_id    TEXT NOT NULL UNIQUE,         -- 种子上的标识（uuid4），撤销与续期用
    device_public_id TEXT NOT NULL DEFAULT '',     -- 所属设备的 public_id
    expires_at       INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX credential_device ON credential (device_public_id);
```

`release` 一行一个平台，两个槽位（启动器、客户端包）的列各自独立写入：发布或清空一个槽位不改动另一个。两个槽位都没有内容时不留行，所以 `releases` 里出现的平台至少发布了一个工件。一次发布请求里的两个槽位在同一个事务里落库（`db.PublishSlots`），任一槽位校验或下载失败则两个都不写。

`credential` 一行是一张签发出去的凭据：`credential_id` 是种子上的标识，`device_public_id` 指向设备，**不存私钥**——私钥只在签发那一次的心跳响应里出现。设备上报的 id 与最新一行一致时，其余行连同种子上的凭据一起撤销；删除设备时先撤销全部凭据再删记录（签发通道未配置时见第 6 节的 `ET_CLI`）。

表结构变化只在 `db/db.go` 的 `createSchema` 里处理：后来新增的列由 `ensureColumn` 逐列补上；旧版 JSON 导入留下的 `device.legacy_id` 列、它的唯一索引与 `setting` 表由 `dropLegacySchema` 删除，导入本身已不存在。

`config_hash` 是数据而不是结构：启动时把每台设备的摘要按当前算法（`core.Hash`，即 `sha256(toml)` 前 16 位）重算一遍，只写不一致的行。摘要算法变化后存量行由此迁移，设备不会停在永远 `stale`、每轮心跳都被重发配置的状态。

## 5. 安全模型

| 面 | 机制 |
| --- | --- |
| 管理面 | `AUTH_TOKEN` 的 Argon2id（m=19456、t=2、p=1）哈希 + 无状态 Bearer（HMAC-SHA256、24h、`SESSION_SIGNING_KEY`）；登录响应有 1s + rand(0..200ms) 的下限时长（`api` 包内常量，不提供关闭开关） |
| 设备面 | Ed25519 请求签名（`MESH2` 八行原文）+ 300 秒时间窗 + nonce 重放缓存；普通节点在 mesh 里的身份是种子签发的凭据，控制面只留 `credential_id` 与到期时间 |
| 重新注册 | 已绑定设备重新注册必须出示签名或有效令牌，只有 `device_id` + 公钥不足以取回含 `network_secret` 的配置 |
| 发布包 | 客户端包：`app_hash`（16 位）用于判定，`app_sha256`（完整）用于下载后校验；启动器：只有 `launcher_sha256`（完整），由客户端自己比对 |
| 发布下载 | 服务端下载发布地址时拒绝回环、私网、链路本地、组播与未指定地址（`net.Dialer.Control` 逐个连接校验），最多 5 次重定向且每一跳重新校验，不走代理 |
| 客户端地址 | 只有 `TRUSTED_PROXIES` 内的对端才能用 `X-Forwarded-For` / `X-Real-IP`，默认不信任任何来源 |
| 输入 | JSON 请求体 1MiB 上限，发布的更新包 200MB 上限，设备数 200 上限，`platform` 的 GOOS/GOARCH 白名单 |
| 响应头 | `X-Content-Type-Options`、`Referrer-Policy`、`X-Frame-Options`、严格 `Content-Security-Policy` |
| 并发 | `/api/auth`、管理面 `/api/admin/*`、设备面 `/api/v1/*`、静态资源、`/readyz` 各自独立信号量（管理面与设备面互不挤占）；Argon2id 并发上限 4 |
| 超时 | 连接读请求头 10 秒、读整个请求 15 秒、空闲 60 秒；发布包下载 60 秒 |

## 6. 环境变量与 Secrets

| 变量 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- |
| `TZ` | 否 | Asia/Shanghai | 日志与时间格式化时区，非法值拒绝启动 |
| `HOST` | 否 | 0.0.0.0 | 与 `PORT` 一起决定监听地址 |
| `PORT` | 否 | `:8088` | 与 `HOST` 一起决定监听地址；未设置时用 `:8088` |
| `DATA_DIR` | 否 | `/data` | 唯一状态目录，`-data` 参数优先级更高 |
| `TRUSTED_PROXIES` | 否 | 空（不信任） | 逗号分隔的 IP 或 CIDR |
| `RELEASE_FETCH_ALLOW` | 否 | 空（只允许公网） | 逗号分隔的 IP 或 CIDR；发布下载允许连接这些内网地址 |
| `ET_CLI` | 否 | 空 | `easytier-cli` 路径；为空表示这台控制面不签发也不撤销凭据，两条凭据路由与「删除还有凭据记录的设备」都返回 409 `credential_channel_unconfigured` |
| `ET_RPC_ADDR` | `ET_CLI` 非空时必填 | — | 种子 management RPC 的地址（宿主上的转发口）；`ET_CLI` 非空而它为空时拒绝启动 |
| `MAX_AUTH_CONCURRENCY` | 否 | 32 | `/api/auth` 并发上限 |
| `MAX_API_CONCURRENCY` | 否 | 128 | 管理面与设备面各自的上限 |
| `MAX_STATIC_CONCURRENCY` | 否 | 64 | 静态资源并发上限 |
| `AUTH_TOKEN` | 是 | — | 管理台唯一凭证，`^[A-Za-z0-9_]{16,128}$`；启动时哈希，明文读完即从环境表移除 |
| `SESSION_SIGNING_KEY` | 否 | 随机 | Bearer 的 HMAC 密钥，`^[A-Za-z0-9_]{32,128}$`；轮换它等于吊销全部已发凭证 |
| `READYZ_TOKEN` | 否 | 空 | 非空时 `/readyz` 必须带同值 token；读入后立即从环境表移除 |
| `GOMEMLIMIT` | 否 | 128MiB | 未设置时由程序设置该软上限 |

数值型变量解析失败或越界时直接拒绝启动，退出码 1。

镜像里的 `easytier-cli` 必须与种子上的 `easytier-core` 同一发布次版本（`Dockerfile` 的 `EASYTIER_VERSION` 固定它）：签发与撤销的参数名、JSON 字段都随版本变化。

## 7. 日志与生命周期

- 日志是 JSON 行，同时写 stdout 与 `DATA_DIR/server.log`；每条访问日志带 `client_ip`、`method`、`path`、`status`、`duration_ms`、`timestamp`；`/livez` 不记；
- 启动成功后打印 `server started`，停止时依次打印 `shutdown signal received`、`server shutdown`；
- 收到 SIGINT/SIGTERM 后用 5 秒上限优雅关闭：超时退出码 254，关闭数据库或日志失败退出码 255，正常 0。