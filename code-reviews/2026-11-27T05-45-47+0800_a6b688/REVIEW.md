# mesh-server 服务端评审（b566879..a6b6880）

- **仓库**：`S:/local/repos/mesh/server`（独立 git 仓库，module `mesh-server`）
- **评审 commit（HEAD）**：`a6b6880545f9255bbb5d1368496a3d72fa59f155`（short `a6b688`，提交时间 2026-11-27 03:05:20 +0800）
- **分支**：`main`
- **评审开始时间**：2026-11-27 05:45:47 +08:00（Asia/Shanghai），目录名 `2026-11-27T05-45-47+0800_a6b688`
- **工作区状态（评审开始时）**：`git status --porcelain` 为空（干净）。本地另有被 `.gitignore` 忽略的 `.data/`、`.dist/`、`data/`、`frontend/dist/`、`.shots/` 与未跟踪的 `k8s.mesh-server.secret.yaml`，均不属评审范围。评审结束时只有 `?? code-reviews/2026-11-27T05-45-47+0800_a6b688/`；**没有修改生产代码、测试代码或文档，没有 commit、没有 push**。
- **评审范围**：上一轮基线 `b566879`（评审目录 `../2026-11-26T07-08-57+0800_b56687/`）到 HEAD 的 16 个提交，外加全项目扫描。前端（`frontend/` 下的 Vue/TS/CSS）按任务要求**不审**；只为了确认接口用法 grep 过一次 `api.ts` 与 `DeviceDetail.vue`。
  1. `83f0f60` 修复评审 F1-F6 并回应 2026-11-26T07-08-57+0800_b56687
  2. `f6d92d2` API JSON 字段统一 camelCase
  3. `be289b3` 注释与文档指向 contract/ 目录
  4. `ebfa133` 设备详情改为行内展开，配置文件编辑拆成弹窗
  5. `4b9f244` 配置文件仅在弹窗内查看与编辑，滚动条常驻避免列表宽度抖动
  6. `058f42f` 滚动区域下移到标题栏以下，滚动条不再顶到标题栏
  7. `7d60b23` 配置模板支持 `{{ IP }}` 注入：创建设备时分配下一个空闲固定地址
  8. `fa7a692` 没有参考地址时从默认网段的 `.1` 开始分配
  9. `ed1bf9a` 配置模板的占位符交给控制台：控制面只存取原文
  10. `7a5483a` 设备级自动更新覆盖：三态覆盖发布包的包级开关
  11. `92917b7` 重置为模板时填入设备当前地址；地址提示不再声称必须唯一
  12. `1e77f90` 心跳 appUpdate 按摘要瘦身：设备已持有同一份就只回版本与开关
  13. `cf1fa8b` 查看与编辑配置与新建同构；template.ts 加反向解析与单测
  14. `2c9e909` 补上 `#app` 的高度链：内容不满一屏时侧栏与表格停在内容末尾
  15. `03c3d22` 文档：契约目录并进 docs/，路径引用跟着改
  16. `a6b6880` 契约文档移到 docs/contract/，路径引用跟着改（本轮 HEAD）
- **对照文档**：mesh 根下 `docs/contract/`（`server.md`、`device-api.md`、`admin-api.md`、`updates.md`、`client.md`）、`docs/README.md`、`../DESIGN.md`（跨两端取舍）、`./DESIGN.md`（实现与部署）、`./README.md`、`../../AGENTS.md`。
- **方法**：通读全部 Go 源码与构建/部署/契约文件 → 逐条核验上一轮 F1–F6 → 对可疑点写 5 个探针（`scripts/`）→ `gofmt -l .`、`go vet ./...`、`go test ./... -count=1`。

## 结论摘要

| 级别 | 数量 | 说明 |
| --- | --- | --- |
| P0 | 0 | — |
| P1 | 0 | — |
| P2 | 2 | 绑定槽位带无效签名时不再尝试有效令牌（与契约「任一凭据」相反）；`ConfirmCredential` 逐张撤销没有总时限，种子变慢时一条心跳可被压住 N×15 秒。 |
| P3 | 7 | 契约数据模型仍写「三张表」（缺 release 表与 device.auto_update）；创建设备静默丢弃 autoUpdate；`/admin/index.html` 被标一年不可变缓存；令牌清理 ticker 在数据库/日志关闭后才停；四处过时注释与不可达分支；只设 HOST 时监听地址忽略 HOST；配置原文没有单独上限而列表接口会全量读入。 |

上一轮 6 条修复全部成立，没有发现由修复引入的新缺陷；本轮 F1/F2 是同一片代码里的边角与时限问题，F3–F9 是契约同步、缓存语义、生命周期与内存边界上的余量。

## 1. 上一轮修复核验

上一轮的评审与回应由 `83f0f60` 一起提交，逐条复核如下（都在 HEAD 上重新读代码 + 跑测试核实，不依赖回应文档的自述）。

| 编号 | 上一轮级别 | 结论 | 证据 |
| --- | --- | --- | --- |
| F1 版本标识 | P2 | 已修，成立 | [app/version.go:48](../../app/version.go#L48) 按（日期，当日序号）排序、[:81](../../app/version.go#L81) 只在「可证明更旧」时下发；版本只来自包内清单 [app/settings.go:372](../../app/settings.go#L372)-[:392](../../app/settings.go#L392)、[:411](../../app/settings.go#L411)-[:447](../../app/settings.go#L447)（zstd+tar 流式找包根 `version.json`，`TeeReader` 同时算整包摘要）；[api/settings.go:105](../../api/settings.go#L105)-[:109](../../api/settings.go#L109) 对请求里出现的 `appVersion` 直接 400。回归 `TestNeedsUpdate`、`TestHeartbeatOnlyUpgrades`、`TestClientPackageVersionComesFromThePackage`、`TestReleaseRejectsIncompletePublish` 全绿。包布局与真实出包脚本一致：`build/release.ps1` 只把 `dist/bin` 的顶层文件写进包根，`version.json` 就在根上。 |
| F2 绝对地址回退 | P3 | 已修，成立 | `api/url.go` 已删除；`grep -E 'absoluteURL|X-Forwarded-Proto'`（排除 `code-reviews/`）零命中；[api/device_api.go:249](../../api/device_api.go#L249) `launcherOf` 只用 `release.LauncherURL`；[DESIGN.md:19](../../DESIGN.md#L19) 已改成「监听地址只来自 HOST/PORT」。发布写入端 [app/settings.go:273](../../app/settings.go#L273)-[:285](../../app/settings.go#L285) 只接受带 host 的 http(s)。 |
| F3 过时注释与清单 | P3 | 已修（另有 4 处仍过时，见本轮 F7） | `justfile:22` 已写「`/readyz` 仍是 200」；`api/api_test.go` 的布局与 headers 注释已改；[app/app.go:128](../../app/app.go#L128) 改为 ticker；`.gitignore` 的四个死路径已删；CI `paths-ignore` 只剩真实文件。 |
| F4 重复实现 | P3 | 已修，成立 | `ReleaseVersion`、`issuedCredential`、`db.Count`、启动时那次令牌清理、导出的 `Authorize` 均已消失（grep 零命中）；[app/credential.go:146](../../app/credential.go#L146)-[:179](../../app/credential.go#L179) 直接返回 `IssuedCredential`。 |
| F5 空白 `default_toml` | P3 | 已修，成立 | [app/settings.go:161](../../app/settings.go#L161)-[:184](../../app/settings.go#L184) 空白即删文件、非空白走 tmp+rename；契约 [docs/contract/admin-api.md:135](../../../docs/contract/admin-api.md#L135) 已写「空或全空白时删除该文件、回退内置模板」；`TestBlankTemplateRestoresBuiltin` 覆盖 `""` 与全空白两种输入。 |
| F6 校验覆盖 | P3 | 已修，成立（两份清单仍是手写） | 管理路由 18 条，两份清单合计 18 条：[api/regress_test.go:82](../../api/regress_test.go#L82)-[:98](../../api/regress_test.go#L98) 12 条 + [api/groups_test.go:229](../../api/groups_test.go#L229)-[:239](../../api/groups_test.go#L239) 6 条；本轮探针 `scripts/probe_admin_route_auth` 用伪造 bearer 走完 18 条：`routes=18 not-401=0`。CI 已加 gofmt 步骤（`.github/workflows/build-and-push-docker-image.yml:37`）。回应里「清单现在 12 条，覆盖 api.go 注册的全部管理路由」是笔误：12 条只覆盖其中一部分，另 6 条在 `groups_test.go`。 |
| U1 一次性迁移 | 待确认 | 未动，仍在 | [db/db.go:157](../../db/db.go#L157) `renameAppHashColumn`、[:177](../../db/db.go#L177) `dropReleaseHashColumn`、[:194](../../db/db.go#L194) `dropLegacySchema` 原样保留，等部署方确认线上/备份库都已迁移。 |
| U2 `app_update.sha256` 的 omitempty | 待确认 | 由本轮设计消解 | 精简形状按契约必须不带 `sha256`（[docs/contract/updates.md:133](../../../docs/contract/updates.md#L133)-[:134](../../../docs/contract/updates.md#L134)），[api/device_api.go:229](../../api/device_api.go#L229) 的 `json:"sha256,omitempty"` 现在有当前用途，不再只是旧数据兼容。 |

## 2. 本轮发现

### [P2][F1] 绑定的槽位带了无效签名就拒绝有效令牌，与契约「任一凭据」相反

**位置**：[api/device_api.go:186](../../api/device_api.go#L186)-[:200](../../api/device_api.go#L200)（`registerProofValid`），调用点 [:141](../../api/device_api.go#L141)；契约 [docs/contract/device-api.md:163](../../../docs/contract/device-api.md#L163)-[:169](../../../docs/contract/device-api.md#L169)。

**触发条件**：槽位已绑定，请求同时带 `timestamp`/`nonce`/`signature` 与一个当前有效的注册令牌，而签名验不过：时钟偏差超过 300 秒、签名是对另一个 `deviceId` 生成的、或这是一次重试（nonce 已被第一次请求 Claim）。

**实际结果**：三个签名字段只要都存在就走签名分支，校验失败直接 `return false`，不会再看令牌。探针 `scripts/probe_register_proof`：

```
TOKEN+BAD-SIGNATURE status=401 body={"code":"unauthorized"}
TOKEN-ONLY           status=200 body={"deviceId":"probe-device-laptop","configToml":"hostname = \"auto\"\n","configHash":"ecafeba6f57c7a6c"}
```

契约写的是「必须出示下列任一凭据：……签名；……注册令牌」，令牌有效时应按 `pubkey` 是否相同回 200 或 409。现在调用方拿到 401，无法与「凭据全无效」区分。

**修改建议**：签名分支失败后继续 `return s.slotTokenValid(device, req.Token, now)`；或者先判令牌、令牌无效再验签。回归用例：绑定的槽位 + 有效令牌 + 签名指向别的 `deviceId` → 200。

### [P2][F2] `ConfirmCredential` 没有总时限：种子变慢时一条心跳被压住 N×15 秒

**位置**：[app/credential.go:210](../../app/credential.go#L210)-[:237](../../app/credential.go#L237)（循环在 [:223](../../app/credential.go#L223)-[:235](../../app/credential.go#L235)）；调用点 [api/device_api.go:311](../../api/device_api.go#L311)-[:315](../../api/device_api.go#L315)（心跳内同步调用，占用 `deviceGate` 槽位）；单次上限 [app/credential.go:26](../../app/credential.go#L26)（15 秒）。对照 [app/credential.go:195](../../app/credential.go#L195)-[:196](../../app/credential.go#L196)：`RevokeCredential` 因为「凭据张数没有上限」加了 30 秒总上限（注释 [:131](../../app/credential.go#L131)-[:133](../../app/credential.go#L133)）。

**触发条件**：设备名下有多张旧凭据（续期后 core 一直没重启、管理端签发后设备没换、上一次撤销部分失败），且 `ET_CLI` 指向的种子 RPC 变慢或不可达。

**实际结果**：`ConfirmCredential` 逐张 `revoke`，每次只受 15 秒单次超时约束，整个循环没有总上限。N 张旧凭据时心跳 handler 最长阻塞 N×15 秒；这期间它占着一个 `deviceGate` 槽位（默认 128），而客户端每 30 秒又发一轮心跳，多台设备同时这样会把设备面推到 503。服务端没有 `WriteTimeout`（[main.go:122](../../main.go#L122)-[:130](../../main.go#L130) 只设了读与空闲超时），所以连接会一直挂着等种子。同一类调用在管理面已经有总上限，两处口径不一致。

**修改建议**：把 `RevokeCredential` 的总时限抽成共享 helper，给 `ConfirmCredential` 套同样的 `context.WithTimeout(ctx, credentialRevokeTimeout)`；超时后返回错误，心跳记日志、留待下一轮。回归用例可用一个每次 `revoke` 都睡 1 秒的假 CLI，断言 3 张旧凭据时心跳耗时不超过总上限。

### [P3][F3] 契约的数据模型停在「三张表」：没有 release 表，device 表也没有 auto_update

**位置**：[docs/contract/server.md:34](../../../docs/contract/server.md#L34)-[:36](../../../docs/contract/server.md#L36)（「数据模型由三张表构成」）、[:38](../../../docs/contract/server.md#L38)-[:60](../../../docs/contract/server.md#L60)（`device` 列清单）；实现是四张表 [db/db.go:15](../../db/db.go#L15)-[:67](../../db/db.go#L67)（`device`、`device_group`、`release`、`credential`），`readyz` 还会建 `__probe`；`device.auto_update` 由 [db/db.go:126](../../db/db.go#L126)-[:129](../../db/db.go#L129) 补列。实现文档 [DESIGN.md:73](../../DESIGN.md#L73)-[:125](../../DESIGN.md#L125) 已经是四张表并带 `auto_update`。

**触发条件**：按契约核对磁盘格式——迁移、备份，或按 `docs/contract/` 写脚本的人。

**实际结果**：契约里查不到 `release` 表（发布记录在磁盘上是 SQLite 行），`device` 表也没有本轮 7a5483a 新增的 `auto_update`。契约被声明为磁盘与线上格式的唯一出处，这里与实现不一致。

**修改建议**：`server.md` 的「数据模型」补上 `release` 表（列声明照 `DESIGN.md` §4）与 `device.auto_update`（三态含义照 `admin-api.md`「设备级自动更新」），把「三张表」改成四张。

### [P3][F4] `POST /api/admin/devices` 静默丢弃 `autoUpdate`

**位置**：[api/devices.go:183](../../api/devices.go#L183)-[:186](../../api/devices.go#L186)（`deviceInput` 只有 `name`/`configToml`）、[:223](../../api/devices.go#L223)-[:247](../../api/devices.go#L247)（handler）；契约 [docs/contract/admin-api.md:127](../../../docs/contract/admin-api.md#L127)（「`POST /api/admin/devices` 不接收这个字段：新设备一律跟随发布包」）。对照同一轮定下的判据：[api/settings.go:105](../../api/settings.go#L105)-[:109](../../api/settings.go#L109) 对请求里的 `appVersion` 直接 400，理由写在 [:39](../../api/settings.go#L39)-[:41](../../api/settings.go#L41)「静默忽略会让调用方以为它填的版本被存了下来」。

**触发条件**：`POST /api/admin/devices` 带 `"autoUpdate": true` 或 `false`（脚本、或把更新接口的请求体整体复用到创建接口）。

**实际结果**：探针 `scripts/probe_device_create_autoupdate`：

```
CREATE status=200 body={"id":"d_34e060","token":"etk_2d91af…"}
DETAIL status=200 autoUpdate=<nil>
PUBLISH appVersion status=400 body={"code":"invalid_argument","message":"appVersion 由包内 version.json 读出，不再作为请求字段"}
```

创建成功、字段被丢掉、设备是「跟随发布包」。调用方拿不到任何提示，两个接口对「不接受的字段」处理方式相反。

**修改建议**：二选一——(a) `deviceInput` 增加一个能区分「缺失」的字段（`*json.RawMessage` 或已有的 `optionalBool`），出现时回 400 `invalid_argument`；(b) 在 `admin-api.md:127` 明确写「该字段被忽略」，并说明为什么与 `appVersion` 不同。

### [P3][F5] `/admin/index.html` 被标成一年不可变缓存，缺失资源回落成 200 HTML

**位置**：[api/pages.go:42](../../api/pages.go#L42)-[:45](../../api/pages.go#L45)（所有存在的文件都设 `Cache-Control: public, max-age=31536000, immutable`）；只有入口 `serveIndex`（[:48](../../api/pages.go#L48)-[:57](../../api/pages.go#L57)）设 `no-store`；未知路径回落见 [:26](../../api/pages.go#L26)-[:41](../../api/pages.go#L41)。

**触发条件**：用 `/admin/index.html` 打开控制台（外部链接、书签、手输）；或升级后用上一版入口文档去请求已经换名的 `assets/*`。

**实际结果**：探针 `scripts/probe_static_index`：

```
/admin                     status=200 cache="no-store" content-type="text/html; charset=utf-8"
/admin/index.html          status=200 cache="public, max-age=31536000, immutable" content-type="text/html; charset=utf-8"
/admin/assets/app-1.js     status=200 cache="public, max-age=31536000, immutable" content-type="application/javascript"
/admin/assets/missing-2.js status=200 cache="no-store" content-type="text/html; charset=utf-8"
```

`index.html` 不是指纹文件名，缓存一年后升级，浏览器仍用旧入口文档加载已经不存在的旧 asset；服务器把缺失资源回成 `index.html`（200 + `text/html`），浏览器按 MIME 拒绝执行，表现为白屏。控制台没有客户端路由，SPA 回落本身没有用途，只会把 404 藏起来。

**修改建议**：`name == "index.html"` 时走 `serveIndex`（`no-store`）；未知路径直接 404（如需保留回落，至少对 `assets/` 前缀回 404）。回归用例：`GET /admin/index.html` 的 `Cache-Control` 是 `no-store`。

### [P3][F6] 令牌清理 ticker 在数据库与日志关闭之后才停

**位置**：[main.go:141](../../main.go#L141)-[:143](../../main.go#L143)（`defer stopPurge()` 与 `go purgeTokens`）、[:167](../../main.go#L167)-[:179](../../main.go#L179)（`Shutdown` → `application.Close()` → `logFile.Close()` → return）、[:149](../../main.go#L149)-[:159](../../main.go#L159)（Serve 失败分支同样）。

**触发条件**：退出时正好落在 `purgeInterval`（1 分钟，[main.go:46](../../main.go#L46)）的 tick 上。

**实际结果**：`stopPurge` 是函数级 defer，实际执行在所有显式关闭之后。窗口内的 tick 会调 `PurgeExpiredTokens` 打到一个已关闭的 `*sql.DB`（`sql: database is closed`），随后把这条错误写向已关闭的日志文件——`slog` 丢弃写错误，于是这行只剩 stdout。进程马上退出，症状只是一条莫名其妙的关闭期错误日志；但顺序本身说明清理 goroutine 没有纳入关闭流程。

**修改建议**：两条退出分支都在 `application.Close()` 之前显式 `stopPurge()`；或改成 `errgroup`/`WaitGroup`，等后台 goroutine 返回后再关数据库与日志。

### [P3][F7] 过时注释、清单与不可达分支

| 位置 | 现在写的是 | 实际是 |
| --- | --- | --- |
| [app/settings.go:129](../../app/settings.go#L129)-[:135](../../app/settings.go#L135) | 「the slots are then committed in one transaction, and the template is staged next to its destination and **renamed after that**」 | 代码先 `staged.Commit()`（[:180](../../app/settings.go#L180)）再 `PublishSlots`（[:186](../../app/settings.go#L186)）；函数内注释 [:175](../../app/settings.go#L175)-[:179](../../app/settings.go#L179) 写的才是实现。空白 `defaultToml` 的 `os.Remove`（[:166](../../app/settings.go#L166)）也在槽位事务之前，同样的窗口没写进注释。 |
| [app/settings.go:45](../../app/settings.go#L45)、[api/api_test.go:301](../../api/api_test.go#L301) | 包由 `client/cmd/pack` 打出 | `client/` 下没有 `cmd/`（旧 Go 客户端的打包器在 `client-prototype/cmd/pack`）；现行出包脚本是 `build/release.ps1`（包根平铺 `dist/bin` 的顶层文件，`version.json` 就在根上）。 |
| [.dockerignore:18](../../.dockerignore#L18)、[:20](../../.dockerignore#L20) | `/AGENTS.md`、`/*.kube` | 仓库里都没有这两个路径（清单是 `k8s.mesh-server.yaml`；`AGENTS.md` 只在 mesh 根）。 |
| [api/main_test.go:12](../../api/main_test.go#L12)-[:16](../../api/main_test.go#L16)、[app/main_test.go:12](../../app/main_test.go#L12)-[:16](../../app/main_test.go#L16) | 「TestMain hands control to the stand-in easytier-cli when the test binary has been re-executed」 | `fakecli.Invoked()` 内部一律 `os.Exit`（[internal/fakecli/fakecli.go:25](../../internal/fakecli/fakecli.go#L25)-[:32](../../internal/fakecli/fakecli.go#L32)、[:45](../../internal/fakecli/fakecli.go#L45)-[:61](../../internal/fakecli/fakecli.go#L61)），`if fakecli.Invoked() { return }` 的 `return` 分支不可达。 |

**修改建议**：逐条改成现状或删除。最后一行直接写 `fakecli.Invoked()`（保留副作用）再 `os.Exit(m.Run())`。

### [P3][F8] 只设 `HOST` 不设 `PORT` 时，监听地址完全忽略 `HOST`

**位置**：[main.go:98](../../main.go#L98)-[:101](../../main.go#L101)（`listen := defaultListen`，只有 `conf.port > 0` 时才 `net.JoinHostPort(conf.host, …)`）、[main.go:34](../../main.go#L34)（`defaultListen = ":8088"`）；契约 [docs/contract/server.md:9](../../../docs/contract/server.md#L9)-[:10](../../../docs/contract/server.md#L10)（「`PORT` 未设置时用 `:8088`」）、[DESIGN.md:159](../../DESIGN.md#L159)（「与 `HOST` / `PORT` 一起决定监听地址；未设置时用 `:8088`」）。

**触发条件**：部署里设了 `HOST=127.0.0.1`（只想让反代从回环访问）但没有设 `PORT`，或 `PORT` 为空。

**实际结果**：探针 `scripts/probe_host_port.ps1`（真实二进制、临时 `DATA_DIR`、8088 空闲）：

```
LOG    {"msg":"server started","addr":":8088",...}
LISTEN 0.0.0.0:8088
LISTEN [::]:8088
```

`HOST` 被静默忽略，服务监听全部地址。契约那句默认值（`:8088`）确实等于「不指定主机」，而 DESIGN 的表述（`HOST`/`PORT` 一起决定）给出的是相反预期——照 DESIGN 配置的人会得到一个对全网开放的控制面。

**修改建议**：`PORT` 未设置时用 `8088` 而不是 `:8088`，让 `HOST` 始终参与 `JoinHostPort`；同步把 `server.md:10` 的措辞改成「未设置时用 `8088`，与 `HOST` 组合」。

### [P3][F9] 配置原文没有单独上限：列表接口把全部设备的配置读进内存

**位置**：[httpx/response.go:12](../../httpx/response.go#L12)-[:13](../../httpx/response.go#L13)（1MiB 请求体是唯一上限）、[api/devices.go:229](../../api/devices.go#L229)（`configToml` 原样入库）、[db/device.go:42](../../db/device.go#L42)-[:44](../../db/device.go#L44)（`deviceColumns` 含 `config_toml`）、[:66](../../db/device.go#L66)-[:84](../../db/device.go#L84)（`List` 全列）、[api/devices.go:151](../../api/devices.go#L151)-[:170](../../api/devices.go#L170)（列表为每台设备算派生字段）；设备上限 200（[app/device.go:15](../../app/device.go#L15)），管理面并发门 128（[api/api.go:85](../../api/api.go#L85)-[:86](../../api/api.go#L86)），`GOMEMLIMIT` 128MiB（[main.go:37](../../main.go#L37)）。

**触发条件**：一批设备写了接近 1MiB 的配置（整段粘贴或脚本生成），然后打开设备页（前端每 5 秒轮询一次）。

**实际结果**：单次 `GET /api/admin/devices` 在内存里同时持有最多 200×1MiB≈200MiB 的配置原文，只为算出 `identity`/`ipv4`/`dhcp` 三个字段；128 个并发请求的上限远高于进程 128MiB 的软上限。派生字段本来就依赖原文，所以约束应放在写入端而不是读取端。

**修改建议**：给 `configToml` 设单独上限（配置只有几 KB，64KiB 足够宽），在 `CreateDevice`/`UpdateDevice` 里按字节拒绝超限，并写进 `admin-api.md` 的字段约束。这样列表与启动迁移的内存才有明确上界。

## 3. 待确认（非 finding）

- **U1 发布摘要可能不是「客户端下载到的字节」**：[app/settings.go:293](../../app/settings.go#L293)-[:309](../../app/settings.go#L309) 的 `newFetchClient` 没有关掉 `Transport.DisableCompression`，Go 会自动加 `Accept-Encoding: gzip` 并透明解压；若包服务器无条件回 `Content-Encoding: gzip`，服务端摘要覆盖的是解压后字节，而客户端按原样下载会校验失败。要复现需要一个强行回该头的服务器，本轮未验证。建议顺手设 `DisableCompression: true`，让摘要等于线上字节。
- **U2 zstd 解码内存与 `GOMEMLIMIT`**：[app/settings.go:411](../../app/settings.go#L411)-[:417](../../app/settings.go#L417) 用 `zstd.NewReader` 的默认选项，窗口上限是 `MaxWindowSize = 1<<29`（512MiB，klauspost/compress v1.20.5 `framedec.go:43`），而进程软上限 128MiB、容器上限 256Mi（`k8s.mesh-server.yaml:70-74`）。只有持管理令牌的人能触发（发布接口），正常包的窗口很小；是否要 `WithDecoderMaxMemory`/`WithDecoderMaxWindow` 收紧，需要确认。
- **U3 「register 成功即清空 token」与实现不符**：[docs/contract/device-api.md:157](../../../docs/contract/device-api.md#L157) 写「`register` 成功即清空 token」；绑定分支用令牌通过校验后只回配置，不清令牌（[api/device_api.go:140](../../api/device_api.go#L140)-[:157](../../api/device_api.go#L157)），令牌在剩余 TTL（≤10 分钟）内仍可反复取配置。是改代码还是改契约，需要确认（影响很小）。
- **U4 契约写死的校验顺序与实现相反**：[docs/contract/device-api.md:40](../../../docs/contract/device-api.md#L40)-[:47](../../../docs/contract/device-api.md#L47) 把「nonce 未被用过」排在「ed25519 验签」之前；[api/device_api.go:91](../../api/device_api.go#L91)-[:99](../../api/device_api.go#L99) 先验签再 Claim。实现顺序更安全（无效签名不会污染 nonce 缓存），建议改契约而不是改代码。
- **U5 归档探针已全部不可复跑**：`f6d92d2` 之后 JSON 字段统一成 camelCase，而 `code-reviews/*/scripts/*` 里的旧探针仍发 `config_toml`/`device_id`，创建设备会 400、注册取不到配置。本轮因此新写了一份 `probeutil`。每次改契约字段名时是否同步历史探针，需要定个约定（与上一轮 U3 同类）。
- **U6 `TemplateTOML` 吞掉所有读取错误**：[app/settings.go:74](../../app/settings.go#L74)-[:83](../../app/settings.go#L83) 对 `os.ReadFile` 的任何错误都回退内置模板；契约只允许「文件不存在或全空白」。文件存在但不可读（权限、IO 错误）时控制台展示内置模板，一保存就覆盖。是否把非 `NotExist` 的错误报成 500，需要确认。
- **U7 `credential_channel_unconfigured` 不在契约错误码表**：三条路由（签发、撤销、删除设备）都会回 409 这个码，`admin-api.md` 的错误码表里没有（只有 `DESIGN.md:163` 提到）。表格本身没声明穷尽，是否补进去由文档维护方定。

## 4. 构建与测试（实测）

**环境**：Windows + pwsh 7；`go version go1.27.4 windows/amd64`（`C:\Program Files\Go\bin` 前置到 PATH）；`GOPROXY=https://goproxy.cn,direct`。

| 命令 | 结果 |
| --- | --- |
| `gofmt -l .`（含本轮新增的 `code-reviews/**`） | 空 |
| `go vet ./...` | exit 0 |
| `go test ./... -count=1` | exit 0：`api 9.987s`、`app 3.851s`、`builtin 1.274s`、`core 1.809s`、`db 3.801s`；其余包与探针无测试；**102 个 `func Test\w+(`**（上一轮 95 个，本轮新增 7 个） |

**本轮探针**（`scripts/`，全部用临时 `DATA_DIR` + `httptest`，不碰本机数据与端口；`go run ./code-reviews/2026-11-27T05-45-47+0800_a6b688/scripts/<名字>` 可重跑）：

| 脚本 | 对应发现 | 结论 |
| --- | --- | --- |
| `probe_register_proof` | F1 | 有效令牌 + 无效签名 → 401；去掉签名 → 200 |
| `probe_static_index` | F5 | `/admin/index.html` 是 `public, max-age=31536000, immutable`；缺失资源 200 + `text/html` |
| `probe_device_create_autoupdate` | F4 | 带 `autoUpdate:true` 创建 → 200，详情 `autoUpdate=null`；同类字段 `appVersion` 在发布接口是 400 |
| `probe_admin_route_auth` | 上一轮 F6 | 18 条管理路由带伪造 bearer 全部 401 |
| `probe_host_port.ps1` | F8 | `HOST=127.0.0.1` 且不设 `PORT`：日志 `addr=:8088`，实际监听 `0.0.0.0:8088` 与 `[::]:8088` |

**未验证/未跑项**：

- 没有跑 `just check`/`just test` 本身（本机没有 `just`，recipe 依赖 bash）；已逐条跑等价命令。
- 没有跑 `pnpm build`/`pnpm typecheck`：会重写被 `go:embed` 的 `frontend/dist`，且前端不在本轮范围。
- 没有跑 `docker build`、CI workflow，也没有连线上种子（ali-seed / tencent-seed / zyf-seed）。
- F2 只用代码与调用链证明，没有造慢 `ET_CLI` 实测阻塞时长（需要额外写一个可控延迟的假 CLI）。
- F1 的客户端另一半（重试时是否真的会同时带令牌与签名）不在本仓库，只按契约文字判定。
- 没有验证 `RELEASE_FETCH_ALLOW` 指向的 mesh 包目录可达性、优雅关闭的 SIGINT 路径（沿用前几轮说明）。

## 5. 收尾

- 评审期间新增：本目录（`REVIEW.md` + `scripts/`：`probeutil`、`probe_register_proof`、`probe_static_index`、`probe_device_create_autoupdate`、`probe_admin_route_auth`、`probe_host_port.ps1`）。Go 探针都已 `gofmt -w`，`go vet ./...` 与 `go test ./...` 通过，不影响 `just test` 的 gofmt 门禁。
- 结束时 `git status --short`：只有 `?? code-reviews/2026-11-27T05-45-47+0800_a6b688/`。没有 commit、没有 push、没有改动任何被跟踪文件。
- 临时产物（`%TEMP%` 下的 `mesh-server-probe-*`）用后即删；本机正在运行的 mesh 客户端与线上主机全程未被触碰。
