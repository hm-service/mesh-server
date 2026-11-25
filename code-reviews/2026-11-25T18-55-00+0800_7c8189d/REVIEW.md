# mesh-server 服务端评审（ea44e6..7c8189d）

- **仓库**：`S:/local/repos/mesh/server`（独立 git 仓库，module `mesh-server`）
- **评审 commit**：`7c8189d1d3117d1b0be7f4d8621378d14b1c14f3`（short `7c8189d`，提交时间 2026-11-25 16:34:46 +0800），分支 `main`
- **上一轮基线**：`ea44e635f7d6afd52cbada0866c807da6b1c8089`（short `ea44e63`，上一轮评审目录 `../2026-11-25T05-05-33+0800_ea44e6/`）
- **评审开始时间**：2026-11-25 18:55:10 +08:00（Asia/Shanghai），目录名 `2026-11-25T18-55-00+0800_7c8189d`
- **评审期间的文件变化（按约定排除在评审范围外）**：`../CONTRACT.md`（19:01:32）、`../DESIGN.md`（19:01:45）、`../docs/client-flows.md`（18:57:36）在评审进行中被并行修改，三者都在本仓库之外（`mesh-app` 根目录不是 git 仓库），改动来自同时进行的客户端侧工作。本仓库（`server/`）内 `git status --short` 全程只有本目录，没有任何被跟踪文件变化。下文凡引用这三份文件的地方，都同时说明了引用的是哪个时刻的版本；上一轮核验以评审开始时的版本（即我第一遍读到的内容）为准。
- **工作区状态（评审开始时）**：`git status --short` 输出为空（干净，无脏文件需要排除）。工作区里另有 `k8s.mesh-server.secret.yaml`，被 `.gitignore:17` 忽略、未跟踪，不属于本轮评审范围。评审结束时只有 `?? code-reviews/2026-11-25T18-55-00+0800_7c8189d/`（本目录）；评审期间**没有修改任何生产代码、测试代码或文档**，没有 commit、没有 push。
- **评审范围**：`ea44e6..HEAD` 的全部提交（9 个）：
  1. `9d640b3` 管理面凭证拆出主清单：Secret 不入库，`/readyz` 不再带 token
  2. `71a620e` 恢复 `/readyz` 的令牌保护：主清单留占位符，部署时渲染真值
  3. `2c70001` 修复评审 2026-11-25T05-05-33+0800_ea44e6 的 F2–F13
  4. `76ac612` 发布下载加白名单网段：`RELEASE_FETCH_ALLOW` 放行内网包目录
  5. `154c17e` 凭据签发与撤销；`config_hash` 改为对原始字节求摘要；镜像构建交给 CI，Dockerfile 增加 `easytier-cli` 阶段
  6. `5baaac1` CI 只推 GHCR
  7. `b53f0d7` 心跳按需签发凭据：设备确认切换后才吊销旧的
  8. `7c8189d` 签发守卫：设备报不出 id 时不再重复签发（本轮 HEAD）
  - 文件范围：后端 `main.go`、`web.go`、`api/`、`app/`、`core/`、`db/`、`httpx/`、`builtin/`、`internal/testenv/`；前端 `frontend/src`；构建与部署 `Dockerfile`、`Dockerfile.runtime`、`.github/workflows/`、`k8s.mesh-server.yaml`、`.gitignore`/`.dockerignore`、`DESIGN.md`。`frontend/dist`、`.dist/`、`data/` 是构建/运行产物，不作为源码评审。
- **对照文档**：`../CONTRACT.md`（1.3 设备签名、1.4 API、1.5 心跳与更新）、`../DESIGN.md`（凭据取代节点私钥、config_hash）、`./DESIGN.md`（部署、接口、表结构、安全模型、环境变量）、`../docs/client-flows.md`。
- **方法**：通读全部源码与文档 → 逐条核验上一轮 F1–F13 → 复跑上一轮 6 个探针 → 针对本轮新代码写 3 个探针 → 用 v2.7.1 官方 `easytier-cli`/`easytier-core`（WSL）核对签发/撤销的调用形式与 JSON 形状 → 全量 gofmt/vet/build/test + 前端 typecheck。
> 约束遵守说明：未部署、未重启任何服务、未连线上主机（ali/tencent/zyf）、未改数据库、未动本机正在运行的 mesh 进程（WSL 里 `/opt/mesh/2026.11.25-54` 的客户端 core 全程未触碰）。所有探针都用临时 `DATA_DIR` + `httptest`，临时目录用后即删。

## 结论摘要

| 级别 | 数量 | 说明 |
| --- | --- | --- |
| P0 | 0 | 上一轮的 P0（凭证明文入库）已拆出且不再入库 |
| P1 | 0 | 上一轮的 P1（`state.json` 复活删除）已彻底删除，探针确认不再复现 |
| P2 | 4 | 凭据签发的收敛性、私钥与 id 可能串台、`config_hash` 存量未迁移、无签发通道时的静默成功 |
| P3 | 5 | 服务端设计文档没跟上凭据实现、凭据子系统零测试、模板写入非原子、删除设备的阻塞调用、部署来源文档自相矛盾 |

本轮最需要处理的是 F2：并发心跳时一条响应可能把 A 的私钥配上 B 的 id，客户端落盘后会把正在用的凭据报成 B，服务端随即撤销 A，设备被踢下线且要等 23 天才会有下一张（已在探针里复现）。F1 是同一段代码的收敛性问题（每轮心跳多签一张）。F3 是 `config_hash` 换算法后存量行没迁移，新装时用内置模板建的设备会永久 `stale` 且每轮心跳被重发配置。

## 1. 上一轮修复核验

上一轮（`ea44e6`）的评审目录由 `2c70001` 一起提交入库（`git log -- code-reviews` 只有这一条），因此可以逐条对照。除特别说明外，结论都由读代码 + 复跑上一轮探针 + 现有测试三方印证。

| 编号 | 上一轮级别 | 结论 | 证据 |
| --- | --- | --- | --- |
| F1 | P0 | 已修（轮换是否完成无法在仓库内验证） | `git ls-files` 已无 secret 文件；`k8s.mesh-server.secret.yaml` 被 `.gitignore:17` 忽略；主清单只剩占位符 `__READYZ_TOKEN__`（[k8s.mesh-server.yaml:89](../../k8s.mesh-server.yaml#L89)） |
| F2 | P1 | 已修 | `app/migrate.go`、`ImportDevices`、`setting` 表全部删除；`probe_legacy_resurrect` 复跑 `NOT FULLY REPRODUCED`；`TestStateJSONIsIgnored` |
| F3 | P2 | 已修（模板这半见本轮 F7） | 两槽位先校验/下载再 `PublishSlots` 单事务；`ClearLauncher` 单事务；`probe_publish_atomicity` 复跑 `NOT REPRODUCED` |
| F4 | P2 | 已修 | `ReadTimeout=15s`；设备面/管理面两个 Gate；`TestDeviceGateDoesNotStarveAdmin`；`probe_slow_body` 复跑 `WHILE-BLOCKED … 200` |
| F5 | P2 | 已修 | nonce 保留到 `max(now,signedAt)+ttl`；`probe_nonce_window` 复跑 `claimed=false` |
| F6 | P2 | 已修 | `AppState` 先判 `r.Hash == ""`；`probe_app_state` 复跑 `app_state=current` |
| F7 | P2 | 已修 | `gofmt -l .` 为空 |
| F8 | P2 | 已修（本轮又出现同类缺口，见 F5） | CONTRACT 1.1/1.4/1.5 与 server DESIGN §3/§4 已补齐分组、布局、两槽位、内网拦截；本轮新增的凭据接口只被并行改动补进了 CONTRACT，本仓库 `DESIGN.md` 仍缺 |
| F9 | P3 | 已修 | `Allow` 按路径真实方法输出；`probe_allow_header` 四条路径全部正确 |
| F10 | P3 | 已修 | 前端 `FALLBACK_TOML` 删除，模板取不到时禁用「生成令牌」并给出重试 |
| F11 | P3 | 已修（新增 `RELEASE_FETCH_ALLOW` 放开面，见待确认 U2） | `dialGuard` 逐连接校验；`TestDialGuard`、`TestPublishRefusesInternalAddresses` |
| F12 | P3 | 已修 | `builtin.Asset/Manifest`、`App.DataDir`、`statusRank`、`decode` 冗余分支均已删除 |
| F13 | P3 | 已修 | 登录下限改为包内常量 `minAuthDelay`，测试通过 `Options.Sleep` 替换等待 |

上一轮 6 个探针本轮原样复跑的原始输出（说明修复没有回退）：

```
probe_legacy_resurrect : NOT FULLY REPRODUCED deviceResurrected=false releaseResurrected=false launcherWiped=false
probe_publish_atomicity: PUBLISH status=400 … NOT REPRODUCED
probe_slow_body        : WHILE-BLOCKED GET /api/devices status=200 … NOT REPRODUCED
probe_nonce_window     : REPLAY … claimed=false / NOT REPRODUCED
probe_app_state        : app_state=current / NOT REPRODUCED
probe_allow_header     : Allow = POST / GET, HEAD, PUT / GET, HEAD, POST / PUT（与真实方法一致）
```

## 2. 本轮发现

### [P2][F1] 心跳签发没有收敛条件：设备一直报旧 id 时每轮心跳再签一张，并发心跳一轮签多张

**位置**：[api/device_api.go:339](../../api/device_api.go#L339)（`case latest.CredentialID != reportedID: need = reportedID != ""`）、[api/device_api.go:343](../../api/device_api.go#L343)（到期续期分支）、[app/credential.go:120](../../app/credential.go#L120)-[app/credential.go:127](../../app/credential.go#L127)（每次签发 = 一次 `easytier-cli` 子进程 + 一条 INSERT）。设计意图见 [../DESIGN.md:47](../../../DESIGN.md#L47)（“设备没收到就再签一张新的，代价是几张废凭据”）。

**触发条件**：

1. 续期（剩余不足 7 天）签出 B 之后，设备在某一轮仍上报 A —— 响应丢失、`credential.json` 写入失败（只读目录、磁盘满）、或 core 切换还没完成又发了一次心跳。此后**每一轮**心跳都命中 339 分支，再签一张；每次签发都要等种子，最坏 15 秒。
2. 同一设备两条心跳并发（客户端的 `connect` 与心跳循环都能发心跳，见 `client/internal/engine/sync.go` 与 `heartbeat.go`，两者没有互斥），一轮就签多张。

**实际结果**（探针 `scripts/probe_credential_reissue`，第一段；每张凭据在种子上有效 30 天，撤销要等设备上报“最新那张”）：

```
FIRST  status=200 credential_id="8439f1ef-…" issued=true
AGAIN  reporting the issued id: status=204 issued=false (want no new credential)
RENEW  status=200 new_id="ebb82775-…" issued=true
OLD-ID round 1: status=200 issued=true id="0496b58a-…"
OLD-ID round 2: status=200 issued=true id="821d8d51-…"
OLD-ID round 3: status=200 issued=true id="97648ba8-…"
OLD-ID round 4: status=200 issued=true id="fa6c229a-…"
TOTAL issued=6 (1 initial + 1 renewal + 4 while the device kept reporting the old id)
REPRODUCED: every heartbeat that reports a stale credential id signs another credential
```

并发段（同一探针第二段）8 条心跳同时发出：`answered_with_credential=8`，即一轮签了 8 张。每张都是种子上的活凭据 + 一条 `credential` 行 + 一次子进程；`7c8189d` 的守卫只挡住了 `reportedID == ""`（旧客户端），对“有 id 但不是最新”没有任何收敛条件。

补充：评审期间被并行更新的 `../CONTRACT.md:172`（19:01:32，不在本仓库、按约定排除在评审范围外）把“不一致时会再签一张发给它”写成了预期行为。也就是说这是**双方都需要知道并划界**的行为，而不是实现单方面的意外：要么在实现里加上限（下一条建议），要么在契约里写清上限与代价。

**修复建议**：给签发加收敛条件，任一即可——(a) `credential` 表记 `last_issued_at`，同一设备两次签发至少间隔 `min(心跳周期 × 2, 5 分钟)`；(b) 续期时只把 `latest` 与“设备上报的那张”配对判断（例如续期签发记下 `renew_of = reportedID`，只有 `reportedID == latest.renew_of` 时才续期）；(c) 设备上报任意已知 id 时不再按“缺凭据”处理，只有“完全没有记录”或“latest 已过期”才签。

### [P2][F2] 心跳响应可能把私钥配到另一张凭据的 id 上：设备随后被撤掉正在用的凭据，失联约 23 天

**位置**：[api/device_api.go:349](../../api/device_api.go#L349)-[api/device_api.go:359](../../api/device_api.go#L359)（签发后重新 `LatestCredential` 取 id）、[app/credential.go:124](../../app/credential.go#L124)（行按插入顺序取 `ORDER BY id DESC LIMIT 1`）、[app/credential.go:163](../../app/credential.go#L163)-[app/credential.go:177](../../app/credential.go#L177)（设备上报的 id 等于 latest 时，撤销“除它之外的全部”）。

**触发条件**：同一设备的两次签发并发（同 F1 的触发条件 2）。

**实际结果**（探针 `scripts/probe_credential_reissue`，第二段）：8 条并发心跳里有 2 条响应把 secret 与 id 配错：

```
MISMATCH secret=secret-49804-… was generated for id=01ac550d-…, response carries id=a3f1bdbd-…
MISMATCH secret=secret-14032-… was generated for id=14e11787-…, response carries id=1d40fb58-…
CONCURRENT heartbeats=8 answered_with_credential=8 distinct_ids=6 cli_calls=14 mismatched_pairs=2
```

后果链条是可从代码证明的：客户端把 `{id: B, secret: 属于 A 的私钥}` 落盘并用 A 起 core（`client/internal/engine/core.go` 的 `--credential`），心跳上报 B。若 B 恰好仍是 latest（探针这次跑出来的那一条 `id=1d40fb58` 就是最后插入的一张），`ConfirmCredential` 会把 A 撤销，而之后再上报 B 与 latest 一致、`ExpiresAt` 还很远，续期分支不触发 —— 设备用一张已被全网撤销的凭据，直到 23 天后进入 7 天窗口才会拿到新凭据；若 B 已被后续签发顶掉，则走 F1 的分支再签一张，多抖动一轮。两种结果都不对，前一种代价最大。

**修复建议**：`IssueCredential` 直接返回 `(credentialID, secret, expiry)`，handler 用返回值而不是再查一次库；[api/devices.go:260](../../api/devices.go#L260) 的 `handleIssueCredential` 同样改成用返回值。

### [P2][F3] `config_hash` 换了算法但存量行没有迁移：内置模板建的设备永久 `stale`，每轮心跳重发配置

**位置**：[core/tomlhash.go:16](../../core/tomlhash.go#L16)（改成对原始字节求摘要）、[db/db.go:104](../../db/db.go#L104)（`createSchema` 只补列/删旧结构，没有任何摘要迁移）、[app/device.go:174](../../app/device.go#L174)（只有管理端改配置时才重算）、[api/device_api.go:285](../../api/device_api.go#L285)（上报值与库里的不一致就下发配置）。

**触发条件**：升级前用旧代码写入的设备行。旧摘要是 `sha256(normalize(toml))`，`normalize` 会补一个末尾换行；而内置模板 `builtin/peer.default.toml` 是 604 字节、**末尾没有换行**（最后一个字节 `0x22`），归一化会补一个，所以“新装部署、`DATA_DIR` 里还没有 `peer.default.toml` 时用内置模板建的设备”必然中招；其它配置只要原文带 CRLF/BOM/行尾空白/末尾多行空行，同样中招。新客户端按磁盘上的原始字节现算（`client/internal/engine/engine.go` 的 `configDigest`），于是永远对不上。

**实际结果**（探针 `scripts/probe_stored_hash`）：

```
TEMPLATE bytes=604 ends_with_newline=false
HASHES   stored_by_old_code=b2fe4ea6666f8ed7 computed_by_new_code=6dd5d6c158c14f95 equal=false
ROW      config_hash=b2fe4ea6666f8ed7 client_reports=6dd5d6c158c14f95 config_state=stale
REPRODUCED: the stored hash was never migrated, so this device stays stale and every heartbeat re-sends its config (api/device_api.go:285)
```

后果：控制台对这批设备永远显示「待同步」，并且每一轮心跳都把几 KB 的配置再发一遍（内容不变，客户端不会重启 core，但状态永远不收敛，运维会被误导）。旧客户端因为上报的是服务端给的 hash，暂时看不出来；等客户端升级到“现算”版本就会暴露。

**修复建议**：在 `createSchema`/迁移里对全部设备做一次幂等重算（`SELECT public_id, config_toml` → 逐行按新算法 `UPDATE`，或直接在 SQL 里无法算就按行更新），并把“摘要算法变了要迁移”写进 DESIGN §4。回归测试：把一行写成旧摘要，`Open` 之后 `config_hash` 应等于新摘要。

### [P2][F4] 没配签发通道时撤销静默成功：删凭据/删设备返回 204，行留在库里、种子上的凭据仍有效

**位置**：[app/credential.go:132](../../app/credential.go#L132)-[app/credential.go:134](../../app/credential.go#L134)（`a.credential == nil` 直接 `return nil`）、[app/device.go:207](../../app/device.go#L207)（删除设备先调它）、[api/devices.go:276](../../api/devices.go#L276)（撤销接口）、[api/devices.go:219](../../api/devices.go#L219)（删除设备的成功分支 204）。

**触发条件**：曾经配过 `ET_CLI` 并签发过凭据，之后从部署里去掉 `ET_CLI`（或把库迁到没配签发通道的环境），再撤销凭据或删除设备。

**实际结果**（探针 `scripts/probe_credential_orphan`；契约侧的承诺见评审期间并行更新的 `../CONTRACT.md:112`“撤销该设备名下全部凭据”与 `../CONTRACT.md:132`“先撤销该设备的全部凭据再删记录：撤销失败就让删除失败”——该文件不在本仓库，按约定不计入评审范围，仅用于核对语义）：

```
ISSUED credential_id=d7de0925-…
REVOKE-ENDPOINT status=204 body=
ROW REMAINS after DELETE /api/devices/{id}/credential (answered success)
DELETE-DEVICE  status=204 body=
ROW REMAINS after DELETE /api/devices/{id}: the credential is now unreachable and unrevokable
REPRODUCED: …
```

两个后果：撤销接口对“凭据仍然可用”这件事回答了成功（项目自己的规范要求“拒绝静默失败”）；设备行删掉之后 `credential` 行再没有任何入口能到达，种子侧那张凭据会一直有效到过期。

**修复建议**：`ET_CLI` 为空时，删除设备与撤销凭据返回明确的失败（如 409 `credential_channel_unconfigured`）并提示运维如何在种子上手工撤销；若允许“只删本地行”，至少要把“未在种子上撤销”写进日志与响应 message，并且仍然删掉本地行。

### [P3][F5] 服务端设计文档没跟上凭据实现：`DESIGN.md` 的 §3 路由、§4 表结构、§6 环境变量都缺，§5 还写着 MESH1

**位置**：实现 —— [api/device_api.go:53](../../api/device_api.go#L53)（请求 `credentialId`）、[api/device_api.go:255](../../api/device_api.go#L255)-[api/device_api.go:257](../../api/device_api.go#L257)（响应 `credential`/`credential_id`/`credential_expiry`）、[api/api.go:145](../../api/api.go#L145)-[api/api.go:146](../../api/api.go#L146)（`POST`/`DELETE /api/devices/{id}/credential`）、[db/db.go:57](../../db/db.go#L57)-[db/db.go:65](../../db/db.go#L65)（`credential` 表）、[main.go:230](../../main.go#L230)-[main.go:234](../../main.go#L234)（`ET_CLI`/`ET_RPC_ADDR`）；本仓库文档缺口 —— [DESIGN.md:36](../../DESIGN.md#L36)（§3 路由表，缺两条凭据路由）、[DESIGN.md:69](../../DESIGN.md#L69)（§4 表结构，自称“`db/db.go` 是唯一的 SQL 位置”，但没有 `credential` 表）、[DESIGN.md:135](../../DESIGN.md#L135)（§6 环境变量表，缺 `ET_CLI`/`ET_RPC_ADDR`）、[DESIGN.md:123](../../DESIGN.md#L123)（§5 仍写 MESH1）。

**触发条件**：按 `server/DESIGN.md` 部署（§6 里找不到签发通道的两个开关）、按 §4 的 schema 建库/迁移、或照着 §3 判断有哪些管理接口。

**实际结果**：`../CONTRACT.md` 在评审开始之后被并行补上了凭据章节（19:01:32，见开头的文件变化声明；按约定不计入本轮评审范围），所以客户端侧不再被卡。但本仓库内的 `DESIGN.md` 三处仍然是旧的：§3 只列到 `POST /api/devices/{id}/token`，没有两条 `credential` 路由；§4 的表结构块没有 `credential` 表，而它自称是 schema 的权威描述；§6 的环境变量表里没有 `ET_CLI`/`ET_RPC_ADDR` —— 而 `ET_CLI` 为空时整套签发/撤销能力就是关闭的（连带 F4 的行为），排障时第一个要看的开关不在文档里。另有一条既存漂移（不是本轮引入）：[DESIGN.md:123](../../DESIGN.md#L123) 仍写“Ed25519 请求签名（`MESH1` 五行原文）”，实际是 `MESH2` 八行（[core/signing.go:26](../../core/signing.go#L26)）。

**修复建议**：§3 补两条路由；§4 补 `credential` 表（`credential_id` 唯一、`device_public_id` 有索引、不存私钥）；§6 补 `ET_CLI`（默认空 = 不签发）与 `ET_RPC_ADDR`（`ET_CLI` 非空时必填，否则拒绝启动）及镜像里 `easytier-cli` 必须与种子 core 次版本一致的约束；§5 把 MESH1 改成 MESH2 并补“设备面还有凭据身份这一层”。

### [P3][F6] 凭据子系统没有任何测试

**位置**：实现 [app/credential.go](../../app/credential.go)、[db/credential.go](../../db/credential.go)、[api/device_api.go:293](../../api/device_api.go#L293)-[api/device_api.go:359](../../api/device_api.go#L359)、[app/device.go:204](../../app/device.go#L204)；测试侧 `api/api_test.go`、`app/device_test.go`、`db/device_test.go`、`app/settings_test.go`。

**实际结果**：全仓 66 个 `Test*`，没有一个覆盖凭据。`app.Options.CredentialCLI` 只在 `main.go:89` 出现，测试环境从不设置，所以“首次签发、确认后撤销旧的、7 天续期、删除设备吊销、`credential` 表读写、无通道时的行为”全部没有回归保护；CONTRACT §3（评审期间被并行更新，见开头声明）只给客户端列了凭据用例（`../CONTRACT.md:287`），服务端的验收项里仍然没有凭据。本轮 F1–F4 全落在这片没有任何测试的代码里，不是巧合。

**修复建议**：用本轮探针的方式（把测试二进制自身当 `easytier-cli`，见 `scripts/probe_credential_reissue`）补表驱动用例；至少覆盖 F1/F2/F4 的行为，避免下次改动再静默漂移。

### [P3][F7] 发布模板的写入不是原子的：固定 `.tmp` 名，且 rename 在槽位事务之后

**位置**：[app/settings.go:136](../../app/settings.go#L136)-[app/settings.go:150](../../app/settings.go#L150)（先 `stageFile`、再 `PublishSlots`、最后 `staged.Commit()`）、[app/atomic.go:23](../../app/atomic.go#L23)（临时文件名是固定的 `path + ".tmp"`）。

**触发条件**：(a) `Commit` 的 rename 失败（磁盘满、权限、目标目录被换）；(b) 两个管理员同时发布且都带 `default_toml`。

**实际结果**：(a) 槽位已经在事务里提交，接口却返回 500 —— 正是上一轮 F3 要消灭的“报错但已生效”，只是搬到了模板这一半；(b) 两个请求写同一个 `peer.default.toml.tmp`，先提交者 rename 走的是后写者的内容，后提交者 `ENOENT` 失败且槽位已生效，模板内容串台。

**修复建议**：临时文件用 `os.CreateTemp(dir, "peer.default.toml.*")` 生成唯一名；把模板 rename 放到事务提交之前（rename 成功再提交事务），或 rename 失败时回滚事务。

### [P3][F8] 删除设备把 `context.Background()` 传进最多 N×15s 的 CLI 调用，忽略调用方取消且没有总时限

**位置**：[app/device.go:207](../../app/device.go#L207)（`a.RevokeCredential(context.Background(), publicID)`）、[app/credential.go:73](../../app/credential.go#L73)（每次调用 15 秒上限）、[app/credential.go:139](../../app/credential.go#L139)-[app/credential.go:146](../../app/credential.go#L146)（逐张串行撤销）、[api/devices.go:218](../../api/devices.go#L218)（handler）。

**触发条件**：设备有多张凭据（F1 的堆积就会造成），且种子 RPC 慢或不可达。

**实际结果**：一次 `DELETE /api/devices/{id}` 最坏耗时 `15s × N`，浏览器/网关早已断开也继续跑；管理面整体不受影响（各连接独立），但这个请求既不会被取消也没有总上限。同类资源问题：每次签发都无上限地起一个子进程（设备面 gate 128，理论并发 128 个 `easytier-cli`），且 `cmd.Output()` 的 stdout 没有大小上限。

**修复建议**：传 `r.Context()` 并给整次撤销一个总上限（例如 30 秒）；或改成“先删本地行 + 记待撤销 id，后台重试”，把管理请求与种子解耦；`run()` 里用 `io.LimitReader` 包一层 stdout。

### [P3][F9] 部署来源的文档与清单自相矛盾

**位置**：[k8s.mesh-server.yaml:48](../../k8s.mesh-server.yaml#L48)-[k8s.mesh-server.yaml:50](../../k8s.mesh-server.yaml#L50)（相邻两行注释互相否定）、[DESIGN.md:15](../../DESIGN.md#L15)（仍写“镜像在目标机本地构建（`Dockerfile.runtime` + 交叉编译好的 `linux/amd64` 二进制），不依赖镜像仓库”）、[Dockerfile.runtime:1](../../Dockerfile.runtime#L1)（已没有任何引用）。

**触发条件**：按 DESIGN §1 部署，或照着清单注释判断镜像从哪来。

**实际结果**：清单实际是 `image: ghcr.io/hm-service/mesh-server:latest` + `imagePullPolicy: Always`（CI 推 GHCR，`5baaac1`/`7c8189d` 之后），紧邻的注释却还写着“本地构建的镜像（见 Dockerfile.runtime），不从仓库拉取”；DESIGN §1 与 `Dockerfile.runtime` 也没有跟着改。照文档做的人会去本机构建，结果部署机上跑的仍是 GHCR 的 `latest`。

**修复建议**：删掉矛盾注释；DESIGN §1 改成“CI 构建推 GHCR、部署机拉取”；明确 `Dockerfile.runtime` 的定位（离线兜底就写清楚怎么用，不用就删）。

## 3. 待确认（非 finding）

- **U1 部署网络来回改，提交信息没提**：`154c17e` 把清单改成 `hostNetwork: true` + `HOST=10.8.0.1` + `PORT=11000`，注释写“等种子侧的凭据链路跑通再换回独立网络命名空间”；`7c8189d` 又把这三处回退了，但提交信息只写“签发守卫”，没有任何部署说明。现在 `ET_RPC_ADDR=10.8.0.1:15889` 要从 apps 网络（10.89.2.0/24）的容器里访问宿主上的转发口。这条链路是否可达、以及这次回退是有意还是误带，需要部署者确认。
- **U2 `RELEASE_FETCH_ALLOW=10.126.126.1/32` 的可达性**：它把内网下载的放行面收窄到一个 mesh 地址（每跳都重新校验，重定向也只能落在该地址内，这一点没问题），但控制面容器能否真的连到 10.126.126.1 上的包目录没有实测；如果连不上，发布内网包会 400 且提示“dial timeout”，看起来像代码错误。
- **U3 没有 `WriteTimeout`**：`ReadTimeout` 只限读，慢读客户端（不读响应体）可以长期占住 `staticGate` 的槽位；静态资源与少量小 JSON 的实际暴露面取决于反代配置，未复现。
- **U4 签发子进程与外发请求的上限**：见 F8。设备面 gate 128 是所有设备共用的，签发慢时 128 条心跳会被占满（每条约 15s 超时）；没有实测。
- **U5 `credential` 表没有过期清理**：过期行只在 `ConfirmCredential`/`RevokeCredential` 时删；设备长期不上线时行会一直留着。若 F1 的堆积真的发生，单设备 30 天内最多可积累 ~86400 行（30s 一轮）。
- **U6 重新绑定（rebind）后旧凭据的撤销依赖新设备心跳确认**：新设备若报空串（正式节点/`network_secret` 身份），`ConfirmCredential` 永远不会为它清掉旧凭据，旧机器的凭据会留到过期。是否可接受需要产品确认。
- **U7 管理端签发/撤销接口没有前端入口也没有文档**：`POST/DELETE /api/devices/{id}/credential` 只能 curl。若定位是运维兜底入口，应写进 CONTRACT 与 DESIGN；若只是过渡，建议删掉，避免两套签发机制并存（F2 的返回 id 问题也在这条路上）。
- **U8 删除设备硬依赖种子可用**：`DeleteDevice` 选择“撤销失败就删除失败”（[app/device.go:204](../../app/device.go#L204) 的注释），代价是种子不可用时无法删除设备、也就无法释放 200 个槽位里的名额。这是有意的取舍，确认是否接受。
- **U9 具体环境值进了仓库**：`k8s.mesh-server.yaml` 里的 `RELEASE_FETCH_ALLOW: 10.126.126.1/32` 与 `ET_RPC_ADDR: 10.8.0.1:15889` 是这一套部署的地址，换机器要改清单。`READYZ_TOKEN` 已经改成占位符+部署时渲染，这两项是否也该同法处理。

## 4. 构建与测试

**环境**：Windows + pwsh 7；`go version go1.27.4 windows/amd64`（`C:\Program Files\Go\bin` 前置到 PATH）；`GOPROXY=https://goproxy.cn,direct`；`pnpm 12.23.0`（`%APPDATA%\npm\pnpm.ps1`）。本机没有 `just`（recipe 还需要 bash），按 `justfile` 逐条跑等价命令，并单独执行了它的 gofmt 门禁判断。

| 实际执行的命令 | 结果 |
| --- | --- |
| `gofmt -l .`（`justfile:47` 的门禁判断） | 空（含 `code-reviews/**` 的探针） |
| `go vet ./...` | 通过（exit 0） |
| `go build ./...` | 通过（exit 0） |
| `go test ./... -count=1` | 通过：`api 6.384s`、`app 1.978s`、`core 1.025s`、`db 2.478s`；其余包与 `code-reviews/**` 的探针无测试文件；共 66 个 `Test*` |
| `pnpm --dir frontend typecheck`（`vue-tsc --noEmit`） | 通过（exit 0） |

**本轮探针**（`scripts/`，全部用临时 `DATA_DIR` + `httptest`，不碰本机数据与端口；`go run ./code-reviews/2026-11-25T18-55-00+0800_7c8189d/scripts/<名字>` 可重跑）：

| 脚本 | 对应发现 | 结论 |
| --- | --- | --- |
| `probe_credential_reissue` | F1、F2 | 复现：上报旧 id 的 4 轮心跳各多签一张（共 6 张）；8 条并发心跳签 8 张，其中 2 条响应的 secret 与 id 不属于同一次签发 |
| `probe_credential_orphan` | F4 | 复现：`ET_CLI` 为空时撤销接口与删除设备都返回 204，`credential` 行仍在 |
| `probe_stored_hash` | F3 | 复现：旧摘要与新摘要对内置模板不同，存量行 `config_state=stale` 且每轮心跳下发配置 |

两个凭据探针把「测试二进制自身」当作 `easytier-cli`：`app.Options.CredentialCLI = os.Executable()`，子进程按 argv 识别 `credential generate/revoke` 并输出与 v2.7.1 相同的 JSON（`credential_id`、`credential_secret`、`expiry_unix` 字符串）。需要两个环境变量：`PROBE_CLI_LOG=<临时文件>`（记录每次签发的 `id secret`，用于核对串台）与 `PROBE_NOW=<unix 秒>`（假时钟起点，同时也是子进程计算 `expiry_unix` 的基准）。

**第三方 CLI 核验**（本轮不只靠读源码）：

- `gh release view v2.7.1 --repo EasyTier/EasyTier` 的资产里有 `easytier-linux-x86_64-v2.7.1.zip` / `easytier-linux-aarch64-v2.7.1.zip`，与 `Dockerfile` 拼出的文件名一致；zip 内条目是 `easytier-linux-x86_64/easytier-cli`，与 `unzip -j … "easytier-linux-${arch}/easytier-cli"` 一致。
- 在 WSL 里用该发布包的 `easytier-core --no-tun -l tcp://0.0.0.0:21099 -r 127.0.0.1:15890` 起一个本地实例，再用 `easytier-cli -p 127.0.0.1:15890 -o json credential generate --ttl 600 --credential-id 11111111-2222-4333-8444-555555555555` 签发，输出：

```json
{"credential_id":"11111111-2222-4333-8444-555555555555","credential_secret":"qOxIDqc5UgYQjQ1N+CYdv0ceRLCiZ6KSOtbX+HEWtzI=","expiry_unix":"1795605049"}
```

  即 `expiry_unix` 确实是字符串（pbjson 的 int64 映射），与 [app/credential.go:49](../../app/credential.go#L49) 的 `Expiry string` 对得上；`credential revoke <id>` 子命令存在。结论：签发/撤销的调用形式与 JSON 解析与 v2.7.1 一致，这一块没有问题。

**未跑/未验证项**：

- `just check` / `just test` 本身没跑（本机无 `just`，recipe 需要 bash）；已逐条跑等价命令并单独执行 gofmt 门禁判断。
- `pnpm build` / `just frontend` 未跑：会重写 `frontend/dist`（被 `go:embed` 嵌进二进制），属于构建资产；类型检查已覆盖。
- 未执行 `podman`/`docker build`，也未跑 CI；只核对了 release 资产名、zip 内路径与本地 CLI 行为。
- 没有连线上种子（ali-server / tencent-server / ali-server-zyf），也没有用真实 mesh 客户端做端到端凭据切换验证；F1/F2 的结论来自 `httptest` + 假 CLI，F3 的结论来自直接读写临时 SQLite。
- 未验证 nginx 后的 `X-Forwarded-*` 行为、`RELEASE_FETCH_ALLOW` 指向的 mesh 包目录可达性、hostNetwork 回退后的网络连通性（U1/U2）。
- 没有验证优雅关闭与 SIGINT 路径（沿用上一轮的说明）。

## 5. 复现环境与收尾

- 评审期间新增：本目录（`REVIEW.md` + `scripts/probe_credential_reissue`、`scripts/probe_credential_orphan`、`scripts/probe_stored_hash`），三个探针都已 `gofmt -w`，`go vet ./...` 与 `go test ./...` 都能通过，不影响 `just test` 的 gofmt 门禁。
- 结束时 `git status --short`：只有 `?? code-reviews/2026-11-25T18-55-00+0800_7c8189d/`。没有 commit、没有 push、没有改动任何被跟踪文件。
- 临时产物已清理：WSL 里的 `/tmp/etrun` 与本地 `%TEMP%` 下的 EasyTier 发布包、解压目录已删除；本机正在运行的 mesh 客户端（含 WSL 里 `/opt/mesh/2026.11.25-54` 的 core）未被触碰。
