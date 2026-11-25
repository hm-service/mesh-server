# mesh-server 服务端评审（7c8189d..b566879）

- **仓库**：`S:/local/repos/mesh/server`（独立 git 仓库，module `mesh-server`）
- **评审 commit（HEAD）**：`b5668798538d4aa1b1da2bab88c5a5e1a81a6db6`（short `b56687`，提交时间 2026-11-26 06:58:27 +0800）
- **分支**：`main`
- **评审开始时间**：2026-11-26 07:08:57 +08:00（Asia/Shanghai），目录名 `2026-11-26T07-08-57+0800_b56687`
- **工作区状态（评审开始时）**：`git status --short` 输出为空（干净）。工作区另有被 `.gitignore` 忽略的构建产物（`.dist/`、`dist/`、`frontend/dist/`、`data/`）与未跟踪的 `k8s.mesh-server.secret.yaml`，均不属于评审范围。评审结束时只有 `?? code-reviews/2026-11-26T07-08-57+0800_b56687/`；**没有修改任何生产代码、测试代码或文档，没有 commit、没有 push**。
- **评审范围**：上一轮基线 `7c8189d`（评审目录 `../2026-11-25T18-55-00+0800_7c8189d/`）到 HEAD 的 12 个提交，外加全项目扫描。前端（`frontend/`、`*.vue`、`*.ts`、CSS）按任务要求**不审**，只把 `pnpm typecheck` 作为构建门禁跑一遍。
  1. `24d2f29` 并发签发用例去掉对调度顺序的依赖
  2. `56a1913` 心跳给报旧 id 的设备补签：管理端签发的凭据靠冷却外的一次心跳下发
  3. `5e795c0` 内置默认模板只描述网络，不再描述节点身份
  4. `cbfe45d` 默认模板不含密钥，新设备一律是凭据节点
  5. `be4015d` 管理面板重做，设备管理加轮换凭据与重置为模板
  6. `9f16799` 内置默认模板改为 aes-gcm 并写入自建 STUN
  7. `6a20726` 默认模板改为固定地址，去掉 DHCP
  8. `a09234e` 默认模板改为三个自建 STUN 端点
  9. `441aec7` 默认模板改用 chacha20
  10. `788c2ad` 移除 `/api/v1/health` 设备健康检查端点
  11. `055209a` 客户端版本标识改用版本号（`last_app_hash` → `last_app_version`，删 `release.hash`）
  12. `b566879` 应用更新增加包级自动安装开关（`release.auto_update` / 心跳 `app_update.autoUpdate`，本轮 HEAD）
- **对照文档**：`../CONTRACT.md`（1.1–1.5、2 节客户端流程、3 节验收）、`../AGENTS.md`（安装目录布局、`bin/version.json`、Windows 分发）、`../DESIGN.md`（根目录设计取舍，仅作参考，不在本仓库）、`./DESIGN.md`（部署、页面、接口、表结构、安全模型、环境变量）、`./README.md`。
- **方法**：通读全部 Go 源码与构建/部署文件 → 逐条核验上一轮 F1–F9 → 复跑上一轮 9 个探针 → 针对本轮改动写 5 个探针 → 全量 gofmt/vet/build/test + 前端 typecheck。

## 结论摘要

| 级别 | 数量 | 说明 |
| --- | --- | --- |
| P0 | 0 | — |
| P1 | 0 | — |
| P2 | 1 | 版本标识只判「是否相等」：设备版本高于发布版本时被永久指示降级；发布标签与包内 `bin/version.json` 不一致时设备永远「有新版本」。 |
| P3 | 5 | 删掉本地托管发布后留下的 `absoluteURL` 兼容分支；多处注释/清单仍在描述已不存在的流程与布局；重复结构与只为测试存在的生产方法；空白模板静默无效；校验覆盖不全。 |

本轮最该处理的是 F1：`auto_update` 打开后，「设备版本比发布版本新」不再只是控制台显示不准，而是每 30 秒被要求装同一个旧包，客户端只能靠本地的降级防护拒绝，双方都不收敛。其余 5 条都是余量与残骸，不是运行期错误。

## 1. 上一轮修复核验

上一轮（`7c8189d`）的评审目录由 `b708155` 一起提交，可以逐条对照。结论由读代码 + 现有测试 + 复跑探针三方印证；**`b708155`/`24d2f29` 之后又有 10 个提交改动了同一片代码，所以每条都在 HEAD 上重新核验过**。

| 编号 | 上一轮级别 | 结论 | 证据 |
| --- | --- | --- | --- |
| F1 | P2 | 仍在（HEAD 形态正确） | 冷却与分档逻辑在 [api/device_api.go:342](../../api/device_api.go#L342)-[api/device_api.go:383](../../api/device_api.go#L383)；`56a1913` 补的「报旧 id 冷却外补签」在 370-376 行。回归测试 [api/credential_test.go:64](../../api/credential_test.go#L64)、[:163](../../api/credential_test.go#L163)、[:214](../../api/credential_test.go#L214)、[:269](../../api/credential_test.go#L269)，`go test` 全绿 |
| F2 | P2 | 仍在 | [app/credential.go:163](../../app/credential.go#L163)-[:186](../../app/credential.go#L186) 返回本次签发的 id/私钥/到期；[api/device_api.go:308](../../api/device_api.go#L308)-[:312](../../api/device_api.go#L312) 只用返回值组响应；并发配对用例 [api/credential_test.go:313](../../api/credential_test.go#L313) |
| F3 | P2 | 仍在 | [app/device.go:287](../../app/device.go#L287)-[:308](../../app/device.go#L308) 启动重算 `config_hash`，[app/app.go:90](../../app/app.go#L90) 调用；用例 [app/device_test.go:234](../../app/device_test.go#L234)；探针 `probe_stored_hash` 复跑 `NOT REPRODUCED` |
| F4 | P2 | 仍在 | 无通道时撤销返回 `ErrCredentialChannelUnconfigured`（[app/credential.go:191](../../app/credential.go#L191)-[:201](../../app/credential.go#L201)），三个入口映射 409（[api/devices.go:255](../../api/devices.go#L255)、[:317](../../api/devices.go#L317)）；用例 [app/credential_test.go:153](../../app/credential_test.go#L153)、[api/credential_test.go:422](../../api/credential_test.go#L422) |
| F5 | P3 | 仍在 | [DESIGN.md:36](../../DESIGN.md#L36)-[:55](../../DESIGN.md#L55) 路由表补了凭据两条；[:104](../../DESIGN.md#L104)-[:124](../../DESIGN.md#L124) 有 `credential` 表；[:152](../../DESIGN.md#L152)-[:168](../../DESIGN.md#L168) 有 `ET_CLI`/`ET_RPC_ADDR`；[:140](../../DESIGN.md#L140) 已是 `MESH2` 八行 |
| F6 | P3 | 仍在（本轮 F6 指出剩余的覆盖缺口） | 测试函数从上一轮 81 个增加到 90 个，凭据子系统有 16 个 `Test*`（[app/credential_test.go](../../app/credential_test.go) 6 个、[api/credential_test.go](../../api/credential_test.go) 10 个） |
| F7 | P3 | 仍在 | [app/atomic.go:21](../../app/atomic.go#L21)-[:41](../../app/atomic.go#L41) 用 `os.CreateTemp` 生成唯一临时名；[app/settings.go:134](../../app/settings.go#L134)-[:150](../../app/settings.go#L150) 先 `Commit` 模板再提交槽位事务，注释写明残留窗口 |
| F8 | P3 | 仍在 | `DeleteDevice` 收 `context.Context`（[app/device.go:204](../../app/device.go#L204)），handler 传 `r.Context()`（[api/devices.go:256](../../api/devices.go#L256)）；撤销总上限 30 秒（[app/credential.go:202](../../app/credential.go#L202)）；stdout 64KiB 上限（[app/credential.go:37](../../app/credential.go#L37)） |
| F9 | P3 | 仍在 | [k8s.mesh-server.yaml:48](../../k8s.mesh-server.yaml#L48)-[:50](../../k8s.mesh-server.yaml#L50) 只剩「CI 构建推 GHCR、部署机拉取」；[DESIGN.md:15](../../DESIGN.md#L15)-[:17](../../DESIGN.md#L17) 同步；[Dockerfile.runtime:1](../../Dockerfile.runtime#L1)-[:4](../../Dockerfile.runtime#L4) 写明是离线兜底且没有 `easytier-cli` |

**上一轮探针复跑（原样执行，未改脚本）**：

```
probe_stored_hash      : TEMPLATE bytes=3317 ends_with_newline=true … equal=true / NOT REPRODUCED
probe_publish_atomicity: PUBLISH status=400 … NOT REPRODUCED
probe_nonce_window     : REPLAY … claimed=false / NOT REPRODUCED
probe_legacy_resurrect : NOT FULLY REPRODUCED deviceResurrected=false releaseResurrected=false launcherWiped=false
probe_allow_header     : /api/auth 405 Allow="POST"；/api/admin/releases 405 Allow="GET, HEAD, PUT"；/api/devices 与 /api/layout 已 404
probe_slow_body        : WHILE-BLOCKED GET /api/devices status=404 … NOT REPRODUCED（这个结论现在没有意义，见下）
probe_credential_reissue / probe_credential_orphan / probe_app_state : panic: POST /api/devices = 404
```

说明（记入待确认 U3）：`be4015d` 把管理路由从 `/api/devices` 改名为 `/api/admin/devices`，上一轮归档的 9 个探针里有 3 个直接 panic、2 个只能跑到 404（`probe_slow_body` 的 `NOT REPRODUCED` 因此是空结论：请求在拿设备面信号量之前就 404 了）。上一轮 F1–F9 的结论因此改为靠 HEAD 的代码与 `go test` 复核，探针只在 `probe_stored_hash` 一条上仍是有效证据。

## 2. 本轮发现

### [P2][F1] 版本标识只比较「不相等」：设备比发布版本新时被永久指示降级，发布标签与包内版本不一致时永远「有新版本」

**位置**：[api/device_api.go:288](../../api/device_api.go#L288)（`appChanged := release.Version != "" && req.AppVersion != release.Version`）、[api/device_api.go:317](../../api/device_api.go#L317)-[:325](../../api/device_api.go#L325)（响应组装）、[app/device.go:334](../../app/device.go#L334)-[:339](../../app/device.go#L339)（`AppState` 只有 `current`/`stale`）、[app/settings.go:216](../../app/settings.go#L216)-[:234](../../app/settings.go#L234)（`validateSlot` 只校验版本号是字符串、地址是 http(s)）、[app/settings.go:286](../../app/settings.go#L286)-[:303](../../app/settings.go#L303)（`fetchDigest` 只算摘要，不看包内容）。布局侧的事实见 [../AGENTS.md](../../../AGENTS.md)：「`bin/version.json` 是唯一版本来源」「zip 里缺这个文件，客户端装上去之后永远被判定为『有新版本』」。

**触发条件**（任一）：

1. 发布记录里的版本比某台设备正在运行的版本旧。真实路径有两条：把发布记录回滚到旧包（回滚材料就在 `packages/<版本>.tar.zst`），以及按 [../AGENTS.md](../../../AGENTS.md)「Windows 客户端分发」手工把 `dist-next/bin` 的 zip 装到某台机器上、而发布记录还是旧的。
2. 发布表单的 `app_version` 与包内 `bin/version.json` 不一致（拼错，或打 zip 的顺序颠倒漏了 `version.json`）。服务端只把整包下载下来算 sha256 就丢弃，从不打开它。

**实际结果**（探针 `scripts/probe_version_identity`，发布 `2026.11.26-1`，设备上报 `2026.11.26-9`）：

```
ROUND 1 status=200 app_update.version=2026.11.26-1 autoUpdate=true reported=2026.11.26-9
ROUND 2 status=200 app_update.version=2026.11.26-1 autoUpdate=true reported=2026.11.26-9
CONSOLE last_app_version=2026.11.26-9 published=2026.11.26-1 app_state=stale
```

两轮心跳都回 200 + `app_update`，指向更旧的包；控制台没有「设备比发布新」这个状态，只有 `stale`，运维看到的是「待更新」而不是「设备超前」。`autoUpdate` 为 true 时客户端每 30 秒被要求装同一个旧包：带降级防护的客户端会拒绝，于是永远循环（每轮下载几十 MB 后丢弃）；没有防护的客户端就是静默降级。第二种触发条件下设备装完仍上报包内版本，于是永远是「有新版本」，`config_state`/`app_state` 都不收敛——正是 [../AGENTS.md](../../../AGENTS.md) 里写明的「永远被判定为有新版本」，区别是现在会自动重复下载安装。

**修改建议**（二选一即可，两条都做也行）：

- (a) 发布客户端包时解析 tar.zst 里的 `bin/version.json`，与表单的 `app_version` 不一致就 400 `invalid_argument`。服务端已经在 `fetchDigest` 里下载整包，只是把字节丢掉了；`ceec06e` 删掉的 `checkReleasePackage` 做的就是这类结构校验。测试夹具 [api/api_test.go:281](../../api/api_test.go#L281)-[:307](../../api/api_test.go#L307) 的包只有 `mesh-client.exe` 与 `bin/easytier-core.exe`，正好是「服务端不校验包内容」的证据。
- (b) 让「是否下发」不再只看相等：按点分段比较版本，只有「设备版本低于发布版本」才下发 `app_update`；`app_state` 增加 `ahead`（或 `unknown`）一档，控制台能区分「待更新」与「设备超前」。这条会改 [../CONTRACT.md](../../../CONTRACT.md) 1.4/1.5 的语义，需要同步改契约。

### [P3][F2] 删掉本地托管发布后留下的 `absoluteURL` 相对地址回退（兼容旧行，且无条件信任 `X-Forwarded-Proto`）

**位置**：[api/url.go:13](../../api/url.go#L13)-[:33](../../api/url.go#L33)，调用点 [api/device_api.go:243](../../api/device_api.go#L243) 与 [:320](../../api/device_api.go#L320)；写入端 [app/settings.go:229](../../app/settings.go#L229)-[:232](../../app/settings.go#L232) 要求地址必须是带 host 的 http(s)。

**背景（可核对的历史）**：`b65f44e` 引入 `absoluteURL` 时，发布包还可以上传到服务端自己的 `/api/updates/` 下，数据库里存的是 `/api/updates/<文件名>` 这样的相对地址；`ceec06e`「更新包只保留外部 URL 发布」删掉了 `/api/updates/` 路由与上传路径，但 `absoluteURL` 的相对分支留了下来。

**触发条件**：只有 `ceec06e` 之前写进库的相对地址行会走到；现在发布不可能再产生相对地址。

**实际结果**（探针 `scripts/probe_relative_release_url`）：

```
PUBLISH relative "/api/updates/windows-amd64-2026.11.26-1-mesh-client.tar.zst" status=400 body={"code":"invalid_argument","message":"app_url 必须是 http(s) 地址"}
PUBLISH relative "mesh-client.tar.zst"                                      status=400 body={"code":"invalid_argument","message":"app_url 必须是 http(s) 地址"}
APP_UPDATE.url="http://127.0.0.1:57265/mesh-client.tar.zst" stored="http://127.0.0.1:57265/mesh-client.tar.zst" equal=true
```

即新写入的行一律命中 `IsAbs` 早退，后面的 `scheme + "://" + r.Host + ref` 与 `X-Forwarded-Proto` 读取永远不会执行。**兼容性**：它为旧行保留了一条把请求的 `Host` 与任意来源的 `X-Forwarded-Proto` 拼进客户端下载地址的回退，而项目明确不做向后兼容。另外 [DESIGN.md:19](../../DESIGN.md#L19) 写「应用只认 `HOST`/`X-Forwarded-Proto` 与可信反代传来的客户端地址」，前半句对 `X-Forwarded-Proto` 不成立（这个函数不看 `TRUSTED_PROXIES`，[httpx/clientip.go:47](../../httpx/clientip.go#L47) 才按可信代理判断）。

**修改建议**：删掉 `api/url.go`，心跳里直接用 `release.URL` 与 `release.LauncherURL`（发布时已保证是绝对地址）；如果确实还有旧行需要照顾，就在迁移里把含相对地址的行清空，而不是留一条拼接回退。同时改掉 DESIGN §1 那一句。

### [P3][F3] 多处注释与清单还在描述已经不存在的流程、布局与产物

**位置与实测**：

| 位置 | 现在写的是 | 实际是 |
| --- | --- | --- |
| [justfile:22](../../justfile#L22) | 「占位版跑起来 `/admin` 是 404、`/readyz` 是 503」 | `/admin` 404 正确；`/readyz` 是 **200**（就绪只看 `DATA_DIR` 与数据库，[api/probe.go:21](../../api/probe.go#L21)-[:34](../../api/probe.go#L34)）。探针 `scripts/probe_stub_frontend`：`GET /admin status=404`、`GET /readyz status=200` |
| [api/api_test.go:999](../../api/api_test.go#L999)-[:1001](../../api/api_test.go#L1001) | 「the launcher is a single file next to the install root, the client package a tar.zst under the **version directory**」 | 现行布局是安装根目录下的 `mesh`/`mesh.exe` + `bin/`（当前版本）+ `packages/<版本>.tar.zst`（回滚材料），**没有版本目录**（[../AGENTS.md](../../../AGENTS.md)「安装目录」一节） |
| [api/api_test.go:207](../../api/api_test.go#L207) | 「signed sends one request carrying device signature **headers**」 | 设备签名早已搬到请求体（[../CONTRACT.md](../../../CONTRACT.md) 1.3「没有任何设备请求再用 `X-` 头认证」），这个 `headers` 参数只用来传业务头 |
| [app/app.go:134](../../app/app.go#L134) | 「`purgeLoop` clears expired registration tokens in the background」 | 没有 `purgeLoop`：函数是同步的 `PurgeExpiredTokens`（[:135](../../app/app.go#L135)），循环在 [main.go:348](../../main.go#L348) |
| [.gitignore:20](../../.gitignore#L20)-[:24](../../.gitignore#L24) | 「旧的编译产物」`/mesh-server`、`/mesh-server.exe`、`/mesh-server-linux`、`dist/` | 现在的产物是 `.dist/mesh-server-<os>-<arch>`（[justfile:7](../../justfile#L7) `DIST := ".dist"`、[:34](../../justfile#L34)-[:35](../../justfile#L35)），这四个路径都没有生产者 |
| [.github/workflows/build-and-push-docker-image.yml:12](../../.github/workflows/build-and-push-docker-image.yml#L12)-[:13](../../.github/workflows/build-and-push-docker-image.yml#L13) | `paths-ignore` 里的 `mesh-server.kube`、`LICENSE` | 仓库里没有这两个文件（清单早已改名为 `k8s.mesh-server.yaml`） |

**修改建议**：逐条改成现状或直接删除。`/readyz` 那条尤其会把排障的人带偏：占位前端起不来时更应该看 `/admin`，而不是以为就绪失败。

### [P3][F4] 重复实现与多余抽象（两套结构、只给测试用的生产方法、两处清理）

**位置**：

- [app/credential.go:26](../../app/credential.go#L26) 的 `issuedCredential` 与 [:154](../../app/credential.go#L154) 的 `IssuedCredential` 字段完全相同（`ID`/`Secret`/`Expiry`），`IssueCredential` 在 [:185](../../app/credential.go#L185) 手工逐字段拷一次；`generate` 直接返回 `IssuedCredential` 即可，少一个类型和一次拷贝。
- [app/device.go:342](../../app/device.go#L342) 的 `ReleaseVersion` 就是 `return r.Version`，两个调用点（[api/devices.go:61](../../api/devices.go#L61)、[:132](../../api/devices.go#L132)）直接取字段即可；`055209a` 之前它还承担「没有发布时返回空」的判断（`r.Hash == ""` 时返回空串），现在那层语义已经并进 `r.Version`。
- [db/device.go:82](../../db/device.go#L82) 的 `Count()` 在生产代码里没有任何调用者，唯一调用是 [db/group_test.go:117](../../db/group_test.go#L117)。这正是「为测试便利给生产代码加成员」：要么删掉让测试自己查，要么让它进生产路径。
- 过期注册令牌的清理有两个入口：启动时 [app/app.go:94](../../app/app.go#L94) 清一遍，[main.go:143](../../main.go#L143) 起的 ticker 每分钟再清一遍（[main.go:348](../../main.go#L348)）。启动那次只覆盖开机到第一次 tick 的一分钟，且过期令牌本来就会被 `TokenExpiresAt` 拒绝，属于同一件事的两套机制。
- [internal/testenv/testenv.go:202](../../internal/testenv/testenv.go#L202) 的导出方法 `Authorize` 注释说给「不能走 `Do` 的测试」用，实际只有同文件 [:196](../../internal/testenv/testenv.go#L196) 的 `Do` 调用它，没有测试用它。

**修改建议**：合并成一个凭据结果类型；去掉 `ReleaseVersion`；`Count` 按上面二选一处理；令牌清理留 ticker 一处（或在启动时清一次并把 ticker 注释成「覆盖运行期」）；`Authorize` 要么改成包内私有函数，要么确有测试要用。

### [P3][F5] 空白 `default_toml` 静默无效：返回 200 但什么也没写，也没有回到内置模板的途径

**位置**：[app/settings.go:135](../../app/settings.go#L135)（`if in.DefaultTOML != nil && !isBlank(*in.DefaultTOML)`）、[api/settings.go:124](../../api/settings.go#L124)（`DefaultTOML: req.DefaultTOML` 原样传下去）。

**触发条件**：`PUT /api/admin/releases` 带 `"default_toml": ""` 或全空白（清空编辑框后保存、或脚本拼出空串）。

**实际结果**（探针 `scripts/probe_blank_template`）：

```
SAVE first status=200
FILE after first save  = "hostname = \"saved\"\n"
CLEAR with ""         status=200 body={"default_toml":"hostname = \"saved\"\n","releases":[]} … file="hostname = \"saved\"\n"
CLEAR with "   \n\t\n" status=200 body={"default_toml":"hostname = \"saved\"\n","releases":[]} … file="hostname = \"saved\"\n"
```

两次都返回 200，模板与文件都没变。两个后果：(a) 调用方拿到成功却没有任何效果，违反项目「拒绝静默失败」的约定；(b) `TemplateTOML`（[app/settings.go:59](../../app/settings.go#L59)-[:68](../../app/settings.go#L68)）把「文件缺失或全空白」当作回退内置模板的条件，但这个接口永远写不出空白文件，于是无法通过接口把模板恢复成内置模板。`../CONTRACT.md` 1.5 只写了「`default_toml` 非空时原子写回」，所以现在这算契约默许，需要一次明确取舍。

**修改建议**：二选一——(a) 全空白返回 400 `invalid_argument`（明确拒绝，前端自己拦清空）；(b) 把全空白定义为「删除 `peer.default.toml`、回退内置模板」，并同步改 CONTRACT 与 DESIGN。

### [P3][F6] 校验覆盖不全：管理面鉴权测试漏 3 条路由，CI 也不跑 gofmt

**位置**：管理路由共 18 条（[api/api.go:138](../../api/api.go#L138)-[:156](../../api/api.go#L156)），鉴权测试的两份清单是 [api/api_test.go:82](../../api/api_test.go#L82)-[:95](../../api/api_test.go#L95) 与 [api/groups_test.go:229](../../api/groups_test.go#L229)-[:239](../../api/groups_test.go#L239)，合计 15 条；缺 `POST`/`DELETE /api/admin/devices/{id}/credential` 与 `DELETE /api/admin/releases/{platform}`。

**实际结果**：探针 `scripts/probe_admin_route_auth` 用伪造 bearer 走完 18 条，全部 401——**代码没有洞**，缺的是回归保护：以后有人把某条路由从 `s.admin(...)` 改成 `s.withGate(...)`，现有测试抓不住这 3 条。`../CONTRACT.md` 3 节写的验收项是「全部管理路由的凭证要求」，与当前的测试覆盖不一致。

另一处门禁不一致：[.github/workflows/build-and-push-docker-image.yml:40](../../.github/workflows/build-and-push-docker-image.yml#L40)-[:44](../../.github/workflows/build-and-push-docker-image.yml#L44) 只跑 `go vet` + `go test`，而 gofmt 门禁只在本地 `just test`（[justfile:47](../../justfile#L47)）；格式不合格的提交能过 CI。

**修改建议**：两份清单补齐 3 条（更稳的做法是让测试从路由注册处生成清单，避免下次再加路由时漏）；CI 加一步 `test -z "$(gofmt -l .)"`。

## 3. 待确认（非 finding）

- **U1 一次性迁移是否删掉**：[db/db.go:152](../../db/db.go#L152) `renameAppHashColumn`、[:172](../../db/db.go#L172) `dropReleaseHashColumn`、[:189](../../db/db.go#L189) `dropLegacySchema` 三处都是「旧结构 → 新结构」的一次性迁移，线上库跑过一次之后就只是空转（每次启动多几次 `PRAGMA table_info`）。项目不做向后兼容，按原则该删；但删掉后任何未迁移过的备份库都无法再启动。需要部署方确认线上/备份库都已迁移。
- **U2 `app_update.sha256` 的 `omitempty`**：[api/device_api.go:221](../../api/device_api.go#L221) 为空时省略，注释与 `../CONTRACT.md` 1.5 都说是为了容忍「早期发布的记录没有这个值」。现在发布路径必写摘要，这条是旧数据兼容，与新原则冲突；是否去掉、或按 U1 一起清旧行，需要确认。
- **U3 上一轮探针已不可复跑**：`be4015d` 把 `/api/devices` 改名为 `/api/admin/devices` 后，上一轮归档的 3 个探针直接 panic、2 个只跑到 404（`probe_slow_body` 的「NOT REPRODUCED」已无意义）。按 skill「每轮目录冻结」这不算缺陷，但意味着「探针可复跑」只对当时那个 commit 成立；是否在改名时把旧探针一并更新，值得定个约定。
- **U4 `X-Forwarded-Proto` 的可信来源**：见 F2。要保留 `absoluteURL` 的话，应改成与 `ClientIP` 一样只在可信代理下采信该头。
- **U5 `default_toml` 只能写不能删**：见 F5，是契约默许还是遗漏，需要产品确认。
- **U6 服务端完全不看发布包内容**：`ceec06e` 删掉 `checkReleasePackage` 之后，tar.zst 里有没有 `bin/version.json`、目录结构对不对，服务端一概不知道；现在这份「包内容由发布者负责」的约定在 `auto_update` 打开后代价更大（见 F1）。确认是否接受。
- **U7 常驻 supervisor 启动器的自更新**：Linux 启动器改成常驻 supervisor 之后，服务端仍通过 `launcher` 槽位下发启动器地址，客户端替换一个正在运行的 supervisor 会怎样，不在本仓库可验证范围（客户端不在这次评审的仓库里）。

## 4. 构建与测试（实测）

**环境**：Windows + pwsh 7；`go version go1.27.4 windows/amd64`（`C:\Program Files\Go\bin` 前置到 PATH）；`GOPROXY=https://goproxy.cn,direct`；`pnpm` 用 `C:\Users\11717\AppData\Roaming\npm\pnpm.cmd`。本机没有 `just`，按 [justfile](../../justfile) 逐条跑等价命令。

| 命令 | 结果 |
| --- | --- |
| `gofmt -l .`（justfile:47 的门禁判断，含 `code-reviews/**` 探针） | 空 |
| `go vet ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `go test ./... -count=1` | exit 0：`api 9.672s`、`app 3.473s`、`builtin 1.008s`、`core 1.389s`、`db 3.271s`；其余包与探针无测试；**90 个 `Test*`**（上一轮 81 个） |
| `pnpm --dir frontend typecheck`（`vue-tsc --noEmit`，仅作门禁，不审前端代码） | exit 0 |

**本轮探针**（`scripts/`，全部用临时 `DATA_DIR` + `httptest`，不碰本机数据与端口；`go run ./code-reviews/2026-11-26T07-08-57+0800_b56687/scripts/<名字>` 可重跑）：

| 脚本 | 对应发现 | 结论 |
| --- | --- | --- |
| `probe_version_identity` | F1 | 设备报 `2026.11.26-9`、发布 `2026.11.26-1`：连续两轮 200 + `app_update{2026.11.26-1, autoUpdate=true}`，控制台 `app_state=stale` |
| `probe_relative_release_url` | F2 | 相对地址两次都 400；绝对地址发布后 `app_update.url` 与存储值逐字相同 |
| `probe_stub_frontend` | F3 | 只有 `STUB` 的嵌入前端下 `/admin=404`、`/readyz=200` |
| `probe_blank_template` | F5 | 空白 `default_toml` 两次都 200 且模板不变 |
| `probe_admin_route_auth` | F6 | 18 条管理路由带伪造 bearer 全部 401 |

**未验证/未跑项**：

- `just check` / `just test` 本身没跑（本机无 `just`，recipe 需要 bash）；已逐条跑等价命令并单独执行 gofmt 门禁判断。
- `pnpm build` 没跑：会重写被 `go:embed` 的 `frontend/dist`，属构建资产；类型检查已覆盖。
- 没有跑 `docker build` / CI，也没有连线上种子（ali-seed / tencent-seed / zyf-seed）。
- 没有用真实客户端验证 F1 的完整链路（客户端侧降级防护、`bin/version.json` 的读取、自动安装循环都不在本仓库），F1 的服务端半边由探针复现，客户端半边依据 `../AGENTS.md` 与 `../CONTRACT.md` 的文字。
- 没有验证 `RELEASE_FETCH_ALLOW` 指向的 mesh 包目录可达性、也没有验证优雅关闭与 SIGINT 路径（沿用上一轮说明）。

## 5. 收尾

- 评审期间新增：本目录（`REVIEW.md` + `scripts/`：`probeutil`、`probe_version_identity`、`probe_relative_release_url`、`probe_stub_frontend`、`probe_blank_template`、`probe_admin_route_auth`）。探针都已 `gofmt -w`，`go vet ./code-reviews/...` 与 `go test ./...` 都能通过，不影响 `just test` 的 gofmt 门禁。
- 结束时 `git status --short`：只有 `?? code-reviews/2026-11-26T07-08-57+0800_b56687/`。没有 commit、没有 push、没有改动任何被跟踪文件。
- 临时产物（`%TEMP%` 下的 `probe-*` 目录与假 CLI 日志）用后即删；本机正在运行的 mesh 客户端与线上主机全程未被触碰。