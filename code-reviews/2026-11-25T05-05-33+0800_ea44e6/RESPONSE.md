# mesh-server 评审响应（2026-11-25T05-05-33+0800_ea44e6）

- **响应对象**：[REVIEW.md](./REVIEW.md)（评审 commit `ea44e635f7d6afd52cbada0866c807da6b1c8089`，分支 `main`）
- **响应时间**：2026-11-25 05:40 +08:00
- **仓库**：`S:/local/repos/dsh_playground/mesh-app/server`（module `mesh-server`）
- **响应时 HEAD**：`71a620e`（`9d640b3`、`71a620e` 是主代理对本轮 F1 的修复，已在响应前）
- **工作区状态**：本轮改动尚未提交时 `git status --short` 为 `M/D` 的源码与文档改动，加上本目录（`REVIEW.md`、六个探针、`RESPONSE.md`）；提交后工作区干净。

## 结论摘要

| 编号 | 级别 | 状态 | 一句话 |
| --- | --- | --- | --- |
| F1 | P0 | 已由主代理修复，本轮未动 | Secret 拆到不入库的 `k8s.mesh-server.secret.yaml`，`/readyz` 令牌仍在 |
| F2 | P1 | 已修（按要求彻底删除） | legacy 导入整条链路删除，`state.json` 不再有任何行为 |
| F3 | P2 | 已修 | 两槽位先校验/下载、再单事务提交；`ClearLauncher` 单事务 |
| F4 | P2 | 已修 | 连接读超时 + 设备面/管理面拆成两个 Gate |
| F5 | P2 | 已修 | nonce 保留到 `max(now, ts)+300s` |
| F6 | P2 | 已修 | 无发布即 `current`，与 CONTRACT 1.4 一致 |
| F7 | P2 | 已修 | `gofmt -l .` 为空 |
| F8 | P2 | 已修 | CONTRACT.md 与 DESIGN.md 补齐接口、字段与表结构 |
| F9 | P3 | 已修 | 405 的 `Allow` 按路径真实方法集合输出 |
| F10 | P3 | 已修 | 删掉前端 `FALLBACK_TOML`，模板取不到就禁用建token |
| F11 | P3 | 已修 | 发布下载拒绝回环/私网/链路本地/组播/未指定地址 |
| F12 | P3 | 已修 | `Manifest`/`Asset`/`DataDir`/`statusRank`/`decode` 冗余分支删除 |
| F13 | P3 | 已修 | 登录下限改为包内常量，测试只替换等待函数 |

## 逐条响应

### F1（P0，凭证入库）—— 已由主代理修复，本轮未动

- 位置：`k8s.mesh-server.secret.yaml`（`9d640b3`）、`k8s.mesh-server.yaml` 的 `__READYZ_TOKEN__` 占位符（`71a620e`）。
- 本轮**没有改** `k8s.mesh-server.yaml`/`.secret.yaml`，也没有往任何入库文件写明文凭证。
- 验证：本机冒烟用 `READYZ_TOKEN` 起服务，`/readyz?token=…` 200、不带 token 401；对应服务端代码未改动（[api/probe.go:22](../../api/probe.go#L22) 仍校验令牌）。

### F2（P1，`state.json` 每次启动重导）—— 已按用户要求彻底删除

删除的兼容代码：

- `app/migrate.go`、`app/migrate_test.go` 整体删除；`app.Open` 不再调用 `importLegacyState` 与 `ImportLegacyRelease`（[app/app.go:41](../../app/app.go#L41)）。
- `db` 层：`ImportDevices`、`ImportRelease`、`ImportLegacyRelease`、`SetRelease`、`SetClientRelease`、`SetLauncherRelease` 删除；`Device.LegacyID` 字段、`deviceColumns`/`scanDevice`/`deviceArgs` 里的 `legacy_id` 一并删除（[db/device.go:19](../../db/device.go#L19)）。`releaseDefaultPlatform` 只有兼容用途，删除（[app/settings.go](../../app/settings.go)）。
- schema：`device.legacy_id` 列、`device_legacy_id` 唯一索引、`setting` 表从 `db/db.go` 的建表语句删除；已有库在启动时由 `dropLegacySchema` 删除（[db/db.go:130](../../db/db.go#L130)：`DROP INDEX IF EXISTS device_legacy_id` → `ALTER TABLE device DROP COLUMN legacy_id` → `DROP TABLE IF EXISTS setting`）。
- 文档：CONTRACT.md 1.1 的 `legacy_id` 行、`setting` 表说明、`state.json` 迁移段，DESIGN.md 的“旧数据迁移”整节与 schema 里的对应列/表都删掉，并重新编号（原 §6/§7/§8 → §5/§6/§7）。

行为：`DATA_DIR/state.json` 不再被读、写、改、删。冒烟时把仓库 `data/state.json`（1 台设备）复制进临时 `DATA_DIR`，启动后 `GET /api/devices` 为 `[]`、`releases` 为 0 条，`state.json` 原样保留。

验证：新增 `TestStateJSONIsIgnored`（[app/settings_test.go](../../app/settings_test.go#L16)）；扩展 `TestOpenMigratesExistingDatabase`（[db/group_test.go:188](../../db/group_test.go#L188)）——老库带 `legacy_id` 列与 `setting` 表，`Open` 后列与表都不存在、设备与分组功能正常。探针 `probe_legacy_resurrect` 现在输出 `devices=0`、`NOT FULLY REPRODUCED`。

### F3（P2，双槽位发布非原子）—— 已修

- `app.Publish`（[app/settings.go:111](../../app/settings.go#L111)）先把请求里的每个槽位校验 + 下载 + 算摘要，全部在内存里完成；任一失败直接返回 400，**此时没有写任何东西**。
- 两个槽位由 `db.PublishSlots`（[db/release.go:93](../../db/release.go#L93)）在**一个事务**里写入：`INSERT ... ON CONFLICT DO NOTHING` 建行 + 每槽一条 `UPDATE`，一起 commit。
- `default_toml` 先用 `stageFile` 写成同目录临时文件（[app/atomic.go:18](../../app/atomic.go#L18)），槽位事务提交后再 `Commit()`（rename）；任一步失败会 `Abort` 删除临时文件，不会留下半份模板。
- `db.ClearLauncher`（[db/release.go:134](../../db/release.go#L134)）的 `UPDATE` 与 `DELETE` 放进同一事务，并发发布不会被清空语句连带删除。
- API 侧只剩一次 `s.app.Publish(...)`（[api/settings.go:115](../../api/settings.go#L115)），不再逐步 `return`。

验证：新增 `TestPublishRejectsBothSlotsTogether`（[api/regress_test.go](../../api/regress_test.go)：client 槽位有效、launcher 槽位不可达 → 400 且两槽位都没落库）、`TestPublishSlotsIsAtomic`（[db/device_test.go](../../db/device_test.go)）。探针 `probe_publish_atomicity` 输出 `PUBLISH status=400` + `NOT REPRODUCED`。

### F4（P2，`/api/*` 无读超时、共享门被占满）—— 已修

- `main.go` 的 `http.Server` 补上 `ReadHeaderTimeout=10s`、`ReadTimeout=15s`、`IdleTimeout=60s`（[main.go:43](../../main.go#L43)、[main.go:119](../../main.go#L119)）：挂住不发送请求体的连接会在 15 秒内被断掉，handler 返回、门释放。
- 设备面与管理面拆成两个 Gate：`deviceGate` 只服务 `/api/register`、`/api/heartbeat`，`apiGate` 服务管理接口（[api/api.go:58](../../api/api.go#L58)、[api/api.go:156](../../api/api.go#L156)）。两者都按 `MAX_API_CONCURRENCY`（默认 128）各自计数，设备面被灌满不再影响控制台。
- DESIGN.md §5 并发与超时、§6 `MAX_API_CONCURRENCY` 说明同步更新。

验证：新增 `TestDeviceGateDoesNotStarveAdmin`（[api/regress_test.go](../../api/regress_test.go)：门设 1 个槽位，一条不发请求体的 `/api/heartbeat` 挂住时 `GET /api/devices` 仍 200）；`internal/testenv` 增加 `WithAPIConcurrency`。探针 `probe_slow_body` 现在输出 `WHILE-BLOCKED … status=200` 与 `NOT REPRODUCED`。

### F5（P2，nonce 与时间窗不匹配）—— 已修

- `core.CheckTimestamp` 改为返回签名时刻 `signedAt`（[core/signing.go:34](../../core/signing.go#L34)），`NonceCache.Claim` 接收 `(nonce, now, signedAt)`，条目过期时间取 `max(now, signedAt)+ttl`（[core/nonce.go:28](../../core/nonce.go#L28)）。
- 两个调用点（心跳验签、已绑定设备重注册）都传入 `signedAt`（[api/device_api.go:80](../../api/device_api.go#L80)、[api/device_api.go:182](../../api/device_api.go#L182)）。
- CONTRACT.md 1.3 的校验描述同步改写。

验证：新增 `TestNonceOutlivesAFutureTimestamp` / `TestNonceExpiresAfterItsWindow`（[core/nonce_test.go](../../core/nonce_test.go)）。探针 `probe_nonce_window` 输出 `REPLAY … claimed=false` 与 `NOT REPRODUCED`。

### F6（P2，无发布时 `app_state` 与契约相反）—— 已修

- `app.AppState`（[app/device.go:303](../../app/device.go#L303)）先判 `r.Hash == "" → current`，再比 `last_app_hash`；未知平台不再单独返回 `stale`。

验证：`TestDerivedStates` 增加“未上报平台 + 无发布 → current”“未上报平台 + 有发布 → stale”两例（[app/device_test.go:208](../../app/device_test.go#L208)）；`TestReportedHashesShowInAdmin` 的首次断言按 CONTRACT 1.4 改为 `app_state=current`。探针 `probe_app_state` 输出 `app_state=current` 与 `NOT REPRODUCED`。

### F7（P2，gofmt 门禁失败）—— 已修

- `gofmt -w` 覆盖全仓，`gofmt -l .` 现在为空（原本缺末尾换行的 `api/device_api.go`、`api/api_test.go` 已修）。

### F8（P2，文档漂移）—— 已修

`../CONTRACT.md`（mesh-app 根目录，直接改文件）：

- 1.1 设备表补 `last_platform`、`group_id`、`position`；新增 `device_group` 表说明；删除 `legacy_id`、`setting` 与 `state.json` 迁移文字。
- 1.2 删除“迁移数据保留旧 hash”的例外，`config_hash` 只有 16 位一种形态。
- 1.4 路由表补齐 `GET /api/template`、`PUT /api/devices/{id}/group`、`PUT /api/layout`、`GET/POST /api/groups`、`PUT/DELETE /api/groups/{id}`；`GET /api/devices` 响应字段补 `group_id`、`position`；新增分组相关规则（`group_not_found`、名字长度、布局去重）、`Allow` 头规则、1MiB 请求体上限；删掉已不存在的“重写 state.json”那条。
- 1.5 补“两槽位一起提交、被拒即不写”“只从公网地址下载、重定向最多 5 次”；1.3 补 nonce 保留规则；第 3 节验收范围更新。

`DESIGN.md`：

- §3 路由表补 4 条分组/布局路由；§4 `DATA_DIR` 去掉 `state.json`，表结构按 `db/db.go` 重写（`device_group` 表、`last_platform`/`group_id`/`position` 列、删除 `legacy_id` 与 `setting`），并说明 schema 升级与兼容对象清理；原 §5“旧数据迁移”整节删除、后续章节重新编号。
- §5 安全模型补“发布下载的地址拦截”“连接与下载超时”“管理面/设备面独立信号量”“登录下限不提供开关”。

### F9（P3，`Allow` 头恒定错误）—— 已修

- `route()` 按路径记录已注册方法（[api/api.go:169](../../api/api.go#L169)），405 兜底闭包调用 `s.methodNotAllowed(w, path)`（[api/middleware.go:91](../../api/middleware.go#L91)），`allowHeader` 按固定顺序输出真实方法（有 `GET` 时补 `HEAD`）。

验证：新增 `TestAllowNamesRegisteredMethods`（[api/regress_test.go](../../api/regress_test.go)）覆盖 `GET /api/auth` → `POST`、`POST /api/admin/releases` → `GET, HEAD, PUT`、`DELETE /api/devices` → `GET, HEAD, POST`、`GET /api/layout` → `PUT`。探针 `probe_allow_header` 四条路径的 `Allow` 均与真实方法一致。

### F10（P3，前端 `FALLBACK_TOML` 是过期副本）—— 已修

- 删除 `frontend/src/format.ts` 的 `FALLBACK_TOML`，默认模板只剩服务端 `DATA_DIR/peer.default.toml` + `builtin/peer.default.toml` 一个事实来源。
- `NewDeviceDialog.vue`：初始 `toml` 为空，`onMounted` 读取 `GET /api/template`；失败时显示“读取默认模板失败，请重试”并提供“重试”，`生成令牌` 按钮禁用；401 走上层 `unauthorized`。

验证：`pnpm --dir frontend typecheck` 通过（`vue-tsc --noEmit`）。未跑 `pnpm build`（会重写 `frontend/dist`）。

### F11（P3，发布地址无 SSRF 防护）—— 已修

- `newFetchClient`（[app/settings.go:233](../../app/settings.go#L233)）：专用 `Transport`（不走代理、10s 拨号、60s 响应头）+ 自定义 `net.Dialer.Control`（[app/settings.go:254](../../app/settings.go#L254)）在**每次连接**（含重定向的每一跳）拒绝未指定、回环、私网、链路本地单播/组播、组播地址；`CheckRedirect` 最多 5 次。
- 校验失败的提示沿用“无法读取 app_url/launcher_url 指向的文件：拒绝连接非公网地址 …”，不再把内网探测结果仅当作网络错误回显。
- 测试注入：`app.Options.FetchClient`（[app/app.go:32](../../app/app.go#L32)）默认为 nil → 用硬化客户端；`internal/testenv` 注入允许回环的客户端，因为发布类测试用 `httptest`（回环）提供安装包。这是下载客户端的依赖注入，不是可关闭的安全开关：不传就是安全默认值。
- DESIGN.md §5 记录该策略；CONTRACT.md 1.5 记录行为。

验证：新增 `TestPublishRefusesInternalAddresses`（回环地址发布 → 400 且不落库）与 `TestCheckDialAddress`（回环/私网/链路本地/未指定/组播拒绝，公网放行）（[app/settings_test.go](../../app/settings_test.go)）。

### F12（P3，死代码）—— 已修

- `builtin.Asset` / `builtin.Manifest` 删除（[builtin/builtin.go](../../builtin/builtin.go)）。
- `app.App.DataDir()` 删除（[app/app.go](../../app/app.go)）。
- `frontend/src/format.ts` 的 `statusRank` 删除。
- `frontend/src/api.ts` 的 `decode` 去掉重复分支，只保留一行 `throw new ApiError(res.status, code)`；需要展示 `message` 的发布接口仍走 `releasesRequest`。

### F13（P3，`MinAuthDelay` 是测试开关）—— 已修

- 登录下限改为 `api` 包内常量 `minAuthDelay = 1s` + `authJitter`（[api/auth.go:15](../../api/auth.go#L15)），`Options.MinAuthDelay` 删除。
- 测试需要快就注入等待函数：`Options.Sleep func(time.Duration)`（[api/api.go:44](../../api/api.go#L44)，nil 时 `time.Sleep`），`internal/testenv` 传空实现；安全参数本身不再可关。

验证：冒烟里 `POST /api/auth` 的访问日志 `duration_ms=1123`，说明下限在生产路径上生效；api 测试仍快（`Sleep` 替换）。

## 待确认项（U1–U9）

- **U1（`absoluteURL` 信任 `X-Forwarded-Proto`/`Host`）**：未改。现在发布地址都必须过 `validateSlot`（绝对 http(s) 地址），legacy 导入已删除，`absoluteURL` 的“相对地址”分支不再有数据来源；如需彻底消除，可另开一轮把 `absoluteURL` 收成只接受绝对地址。
- **U2（迁移的 `app_url` 没过 `validateSlot`）**：随 F2 的导入删除而消失，没有迁移路径了。
- **U3（心跳字段无长度上限）**、**U4（内存 map 无上界）**、**U5（关闭顺序）**、**U6（没有 panic 恢复中间件）**、**U7（nginx 可能掩盖 F4）**、**U8（前端凭证存放，复核无问题）**、**U9（`window.__app`）**：本轮未改。都不是评审的 finding；U3/U4/U5/U6 需要产品层面的取舍（字段上限会不会破坏现有客户端、map 上限策略、退出竞态是否值得加同步、panic 是否要显式 500），U7 取决于部署环境，U8/U9 是可接受现状。

## 构建与测试

环境：Windows + pwsh 7，`go version go1.27.3 windows/amd64`，`pnpm 12.23.0`（`$env:APPDATA\npm\pnpm.cmd`），`GOPROXY=https://goproxy.cn,direct`。本机仍没有 `just`（recipe 还要 bash），按 `justfile` 逐条跑等价命令。

| 命令 | 结果 |
| --- | --- |
| `gofmt -l .` | 空（F7 已修） |
| `go vet ./...` | 通过（exit 0） |
| `go build ./...` | 通过（exit 0） |
| `go test ./... -count=1` | 通过：`api 6.751s`、`app 2.243s`、`core 0.917s`、`db 2.799s`；`mesh-server`、`builtin`、`httpx`、`internal/testenv` 与 `code-reviews/**` 的探针无测试文件 |
| `pnpm --dir frontend typecheck` | 通过（exit 0） |

测试数量：63 → 67 个 `Test*` 函数。新增 9 个、删除 1 个（`TestImportDevicesIsIdempotent`，随 `ImportDevices` 删除），另有 3 个既有测试调整断言（`TestReportedHashesShowInAdmin` 的 `app_state`、`TestReleasePerPlatform`/`TestReleaseSlotsAreIndependent` 改用 `PublishSlots`）。新增：`TestStateJSONIsIgnored`、`TestPublishRefusesInternalAddresses`、`TestCheckDialAddress`、`TestNonceOutlivesAFutureTimestamp`、`TestNonceExpiresAfterItsWindow`、`TestPublishSlotsIsAtomic`、`TestPublishRejectsBothSlotsTogether`、`TestAllowNamesRegisteredMethods`、`TestDeviceGateDoesNotStarveAdmin`。

冒烟（同一份 `go build` 产物，临时 `DATA_DIR` 里放一份 `data/state.json`，`HOST=127.0.0.1 PORT=18099`，探针凭证）：`/livez` 200；`/readyz?token=…` 200；`/api/health` 200；`/api/devices` 无凭证 401；带 Bearer 200 且 `[]`（旧 `state.json` 里的 1 台设备没有复活）；`/api/admin/releases` 200、`releases` 为空、`default_toml` 为空串；`state.json` 仍在且未被修改；进程退出后临时目录已删除，本机正在运行的 mesh 进程未被触碰。

探针复跑（`go run ./code-reviews/2026-11-25T05-05-33+0800_ea44e6/scripts/<名字>`，全部用临时 `DATA_DIR`）：

| 探针 | 修复前 | 现在 |
| --- | --- | --- |
| `probe_legacy_resurrect` | 删除设备/发布后重启复活 | `devices=0`、`release_found=false`、launcher 槽位保留 → `NOT FULLY REPRODUCED` |
| `probe_publish_atomicity` | 400 但 client 槽位已生效 | `PUBLISH status=400`、`NOT REPRODUCED` |
| `probe_slow_body` | 控制台 503 `busy` | `WHILE-BLOCKED … status=200`、`NOT REPRODUCED` |
| `probe_nonce_window` | 时间窗内重放被接受 | `claimed=false`、`NOT REPRODUCED` |
| `probe_app_state` | 无发布时 `stale` | `app_state=current`、`NOT REPRODUCED` |
| `probe_allow_header` | 恒 `GET, HEAD` | `POST` / `GET, HEAD, PUT` / `GET, HEAD, POST` / `PUT` |

## 本轮对评审目录的改动

除新增本文件外，改了本目录里三个探针（其他探针与 `REVIEW.md` 未动），原因都是**编译或判据**，不改评审结论：

- `scripts/probe_nonce_window/main.go`：`CheckTimestamp` 现在返回 `(signedAt, error)`、`Claim` 多了第三个参数，按新签名适配。
- `scripts/probe_legacy_resurrect/main.go`：`SetLauncherExternal` 已删除，改用 `app.Publish`；启动时注入允许回环的 `FetchClient`（探针用 `httptest` 提供安装包）；删除 legacy 设备时容忍 `ErrNotFound`（导入已不存在）。
- `scripts/probe_slow_body/main.go`：原判据在“连接关闭后”的 200 上判 `REPRODUCED`（结论恒为复现），改成看“连接仍打开时”的状态码；`WHILE-BLOCKED` 一行的数据本身没变。

## 未自动验证项

- `just check` / `just test` 本身未跑（本机无 `just`），已逐条跑等价命令。
- `pnpm build` / `just frontend` 未跑（会重写被 `go:embed` 的 `frontend/dist`）；类型检查已覆盖。
- 按约束未做 `podman build`、未部署、未连线上主机、未重启本机 mesh 进程；未验证真实 nginx 后的 `X-Forwarded-*` 行为。
- 发布下载使用**直连**（不走代理）：如果部署环境必须通过正向代理出网，需要再补一个显式的代理配置项，这项没有环境可验证。
- 已有 SQLite 库的 `ALTER TABLE device DROP COLUMN legacy_id` 依赖 modernc.org/sqlite 1.63.0（SQLite ≥ 3.35）；单元测试用旧 schema 构造库验证过，但没有用线上库副本跑过。
