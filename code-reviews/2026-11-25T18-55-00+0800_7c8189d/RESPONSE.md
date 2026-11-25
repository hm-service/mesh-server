# mesh-server 修复响应（评审 2026-11-25T18-55-00+0800_7c8189d）

- **响应对象**：同目录 [`REVIEW.md`](REVIEW.md)（评审 commit `7c8189d`；P0 0 / P1 0 / P2 4 / P3 5，F1–F9）
- **响应时间**：2026-11-25 19:21 +08:00（Asia/Shanghai）；19:25 按 lead 决策补 `none` 签发冷却，19:29 把空串判断提前到读库之前，19:32 并发用例改为容忍冷却回 204（以上在提交 b708155 / 24d2f29）；19:45 补「报旧 id 冷却外补签」让管理端签发的凭据能下发（单独提交）
- **修复提交**：本目录与下列改动一起提交，commit message 引用 `2026-11-25T18-55-00+0800_7c8189d`
- **环境**：Windows + pwsh 7；`go version go1.27.4 windows/amd64`（`C:\Program Files\Go\bin` 前置）；`GOPROXY=https://goproxy.cn,direct`；`pnpm 12.23.0`
- **约束遵守**：只改 `server/` 内文件，以及根目录 `CONTRACT.md` 1.5 的凭据段与 `DESIGN.md` 凭据段的一句冷却说明；未改 `client/`；未部署、未连种子、未跑 podman/CI、未动 k8s 线上状态。

## 逐条响应

### [P2][F1] 心跳签发的收敛条件

**位置**：[api/device_api.go:339](../../api/device_api.go#L339)（`issueCredentialIfNeeded`）、[api/device_api.go:300](../../api/device_api.go#L300)（响应组装）；续期窗口常量在 [app/credential.go:129](../../app/credential.go#L129)（`CredentialRenewBefore` = 7 天，与客户端同一口径）。

**修复**：签发条件按 `credentialId` 分档，不再「id 不一致就签一张」：

- `credentialId == ""` → 不签发也不确认（正式节点，或还不认识这个字段的旧客户端）。这个判断排在读凭据表之前，所以「这台设备没有任何凭据记录」也不会被「无记录就签一张」抢先白签。
- 记录里没有任何凭据 → 签发（这台设备从来就没有过凭据）。
- `credentialId == "none"` → 签发（客户端明确声明本地没有凭据），同一设备 5 分钟内最多一张。
- 与记录里最新签发的 id 一致：先由 `ConfirmCredential` 确认切换（只有一致时才真的撤销其余凭据）；这是常态，只有剩余有效期不足 7 天时才续签一张。
- 有值但不是最新：设备还在用旧的那张。冷却内不签（它多半正在重启 core，等它报上最新那张就收敛）；冷却外补签一张——管理端 `POST /api/devices/{id}/credential` 签的凭据只能靠这一步落到设备上，客户端卡在旧凭据上也会在冷却后拿到新的。
- 冷却：同一设备距最近一条凭据的 `created_at` 不足 5 分钟时，上面所有补签都不发生；它挡的是并发心跳、本地写盘一直失败和每轮心跳都报旧 id 时的重复签发。

原来的 `case latest.CredentialID != reportedID: need = reportedID != ""` 已删除：上报的 id 与最新不一致时不再无条件补签，改为看冷却——冷却内等 core 重启后收敛，冷却外才补一张（管理端签发靠的就是这条）。

**冷却（`none` 分支）**：同一设备距最近一条凭据的 `created_at` 不足 5 分钟时，即使报 `none` 也不再签发，直接回 204（[api/device_api.go:349](../../api/device_api.go#L349)，常量 [app/credential.go:136](../../app/credential.go#L136)）。冷却同时管 `none` 与「报旧 id」两条补签路径：并发心跳、本地文件一直写不进去、设备卡在旧 id 上时，每 30 秒一轮的补签都会被它挡住；正常恢复（重装、凭据丢失）第一轮就拿到，管理端签发后最多等 5 分钟。它读的是最近一条的创建时间而不是原子占位，所以设备**一条凭据记录都没有**时的并发首轮仍会各签一张（没有可比较的时间），稳态下的重复补签被挡住。冷却命中时不调 `ConfirmCredential`（[api/device_api.go:295](../../api/device_api.go#L295)）——那张没被设备用上，确认会吊销正在生效的旧凭据。

**验证**：新增 [api/credential_test.go:63](../../api/credential_test.go#L63) `TestHeartbeatIssuesCredentialOnlyWhenNeeded`：首次 `none` 签发；上报最新 id → 204；上报一个旧 id（冷却内）→ 204 且 `LatestCredential` 不变；上报空串 → 204 且不变；时钟推进到续期窗口后上报旧 id → 恰好一张新凭据（id 与私钥配对）；再上报新 id → 204。另新增 [api/credential_test.go:144](../../api/credential_test.go#L144) `TestEmptyCredentialIDNeverIssues`：一台从未签发过的设备首心跳报空串 → 204，且 `LatestCredential` 仍是 `ErrCredentialNotFound`（空串永远不会被白签）；[api/credential_test.go:162](../../api/credential_test.go#L162) `TestNoneCooldown`：连续两次 `none`，第二次 204 且最新 id 不变；推进 5 分钟后再次 `none` 签发一张（id 与私钥配对）；[api/credential_test.go:213](../../api/credential_test.go#L213) `TestAdminIssuedCredentialReachesDevice`：管理端另签一张、设备仍报旧 id → 冷却内 204，推过 5 分钟后心跳补签一张并下发（配对校验）；[api/credential_test.go:268](../../api/credential_test.go#L268) `TestRenewalIsNotRepeated`：续期签出下一张后，设备报旧 id → 冷却内 204；改报刚续期的那张 → 204，最新 id 不变（不会反复续签）。

**探针复跑**：`probe_credential_reissue` 本轮改了两处（探针是我们自己的验证脚本，不动生产代码）：

1. 首心跳从空串改成 `none`。原来那写法是照着当时的实现写的，契约里空串永远不签发，所以探针的 FIRST 段与新口径不符，已按契约改成普通节点的 `none`。
2. 假 CLI 的 `expiry_unix` 改成跟随推进后的 `PROBE_NOW`（`clock.Advance` 里同步环境变量）。原来 expiry 固定以起始时刻为基准，`Advance(24d)` 之后每张凭据（包括刚续期的那张）都只剩 6 天，续期窗口永远关不上，第一段无论实现对错都会打印 `REPRODUCED`，判不了 F1。

改后输出：`FIRST status=200 issued=true` → `AGAIN status=204 issued=false` → `RENEW status=200 issued=true` → `OLD-ID round 1–4 status=204 issued=false` → `TOTAL issued=2`、`NOT REPRODUCED`。第二段 `mismatched_pairs=0`、`NOT REPRODUCED: every response paired its secret with the id it was generated for`；它的 `answered_with_credential=8` 是 8 条并发心跳在一台「没有任何凭据记录」的设备上首轮各签一张（没有可比较的创建时间，冷却按设计不生效），属于取舍 3。

### [P2][F2] 私钥与 `credential_id` 串台

**位置**：[app/credential.go:159](../../app/credential.go#L159)（`IssueCredential`）、[api/device_api.go:334](../../api/device_api.go#L334)、[api/devices.go:253](../../api/devices.go#L253)（`handleIssueCredential`）。

**修复**：`IssueCredential` 返回 `app.IssuedCredential{ID, Secret, Expiry}`，三个值都取自本次 `generate` 的结果；心跳与管理端签发接口都用这个返回值组装响应，删掉了签发后再查一次库的两处（原 `issueCredentialIfNeeded` 末尾与 `handleIssueCredential` 里的 `LatestCredential`）。

**验证**：[api/credential_test.go:312](../../api/credential_test.go#L312) `TestConcurrentIssuanceKeepsCredentialPairs`：6 条并发心跳，凡签发了的（200）逐条断言「响应里的私钥就是响应里那个 id 签出来的」（假 CLI 把 id 编进私钥，测试解出来比对）且 id 互不相同；冷却命中回 204 的按设计跳过，只要求至少一条签发。并发里谁先落库由调度决定，只有它之前的读能过冷却，所以不能断言每条都 200——那正是这条用例最初在 Linux CI 上挂掉的原因；维护性上它验的是「已签发的那些没有串台」；[app/credential_test.go:35](../../app/credential_test.go#L35) `TestIssueCredentialReturnsItsOwnID`、[api/credential_test.go:395](../../api/credential_test.go#L395) `TestAdminIssueCredentialReturnsItsOwnID` 覆盖 app 层与管理端。探针第二段 `mismatched_pairs=0`（修复前 2）。

### [P2][F3] `config_hash` 存量行迁移

**位置**：[app/app.go:90](../../app/app.go#L90)（`Open` 里调用）、[app/device.go:289](../../app/device.go#L289)（`migrateConfigHashes`）；文档在 [DESIGN.md:132](../../DESIGN.md#L132)。

**修复**：启动时把每台设备的 `config_hash` 按 `core.Hash(config_toml)` 重算，只 `UPDATE` 不一致的行，`config_toml` 原样不动。幂等，每次启动都可以跑；摘要算法以后再变也走同一条路径。

**验证**：[app/device_test.go:234](../../app/device_test.go#L234) `TestOpenRehashesStoredConfigs`：写入按旧算法（`sha256(toml + "\n")`）算出的摘要，`Open` 之后 `config_hash == core.Hash(toml)`。探针 `probe_stored_hash` 重跑：`ROW config_hash=6dd5d6c158c14f95 client_reports=6dd5d6c158c14f95`、`NOT REPRODUCED`（修复前是 `b2fe4ea6666f8ed7` 对 `6dd5…`）。探针里 `config_state=stale` 属于正常语义（`last_config_hash` 还是空，表示这台设备从未上报），第一次心跳之后即 `current`。

### [P2][F4] 没有签发通道时的静默成功

**位置**：[app/credential.go:181](../../app/credential.go#L181)（`RevokeCredential`）、[app/credential.go:144](../../app/credential.go#L144)（新错误）、[api/devices.go:292](../../api/devices.go#L292)（409 响应）、[api/devices.go:218](../../api/devices.go#L218)/[api/devices.go:253](../../api/devices.go#L253)/[api/devices.go:276](../../api/devices.go#L276)（三个入口的映射）。

**修复**：`RevokeCredential` 先查记录：没有记录 → 直接成功（没有凭据的设备不需要通道也能删）；有记录而没有 `ET_CLI` → 返回 `ErrCredentialChannelUnconfigured`，不再 `return nil`。撤销凭据、删除设备、管理端签发三个入口把它映射成 `409 {"code":"credential_channel_unconfigured","message":"…请在种子上手工撤销这些凭据"}`。行仍留在库里（刻意的：种子侧那张凭据还没被撤销，删掉行只会让它再也到不了），但接口不再回答成功。[DESIGN.md:159](../../DESIGN.md#L159) 的 `ET_CLI` 行写明了这条 409。

**验证**：[app/credential_test.go:153](../../app/credential_test.go#L153) `TestRevokeCredentialRequiresChannel`（有记录 + 无通道：撤销与删除都返回该错误、行与设备都还在；没有凭据的设备照删）；[api/credential_test.go:421](../../api/credential_test.go#L421) `TestCredentialChannelLossBlocksRevokeAndDelete`（同一 `DATA_DIR` 先带 CLI 签发、再不带 CLI 起服务：两个 DELETE 都是 409 + 该 code，设备仍在）。探针 `probe_credential_orphan` 重跑：`REVOKE-ENDPOINT status=409`、`DELETE-DEVICE status=409`（修复前都是 204 + 行留下）；探针末尾那句 `REPRODUCED` 是它只看「行是否留下」的旧判据，行是刻意留下的，现在的判据应是状态码。

**取舍（已确认接受）**：不改成「先删本地记录 + 后台重试撤销」。凭据还在种子上就是还能连，先删记录只会让控制面失忆、设备照样在网络里；管理员该做的是把 `ET_CLI` 修好。

### [P3][F5] `server/DESIGN.md` 跟上凭据实现

**位置**：[DESIGN.md:15](../../DESIGN.md#L15)-[DESIGN.md:17](../../DESIGN.md#L17)（`1`）、[DESIGN.md:44](../../DESIGN.md#L44)-[DESIGN.md:46](../../DESIGN.md#L46)（`3`）、[DESIGN.md:116](../../DESIGN.md#L116) / [DESIGN.md:128](../../DESIGN.md#L128) / [DESIGN.md:132](../../DESIGN.md#L132)（`4`）、[DESIGN.md:139](../../DESIGN.md#L139)（`5`）、[DESIGN.md:159](../../DESIGN.md#L159)-[DESIGN.md:160](../../DESIGN.md#L160) / [DESIGN.md:171](../../DESIGN.md#L171)（`6`）。

**修复**：`3` 补 `POST / DELETE /api/devices/{id}/credential` 两条路由；`4` 补 `credential` 表（`credential_id` 唯一、`device_public_id` 索引、不存私钥）与撤销/删除的行为说明，并补 `config_hash` 启动重算；`5` 设备面从 `MESH1` 五行改成 `MESH2` 八行，并写明普通节点另有种子签发的凭据身份；`6` 补 `ET_CLI`（空 = 不签发不撤销，两条凭据路由与删除还有凭据记录的设备都返回 409）与 `ET_RPC_ADDR`（`ET_CLI` 非空时必填，否则拒绝启动），以及「镜像里的 `easytier-cli` 必须与种子 core 同一发布次版本」。

**验证**：人工逐条对照实现——路由 [api/api.go:145](../../api/api.go#L145)-[api/api.go:146](../../api/api.go#L146)、表 [db/db.go:57](../../db/db.go#L57)-[db/db.go:65](../../db/db.go#L65)、环境变量 [main.go:230](../../main.go#L230)-[main.go:234](../../main.go#L234)。文档没有自动校验。

### [P3][F6] 凭据子系统测试

**位置**：新增 [internal/fakecli/fakecli.go](../../internal/fakecli/fakecli.go)（把测试二进制自身当 `easytier-cli`：`Invoked` 在 `TestMain` 里先于 testing 解析参数接管，`Owner` 把私钥里编码的 id 解出来）、[internal/testenv/testenv.go:88](../../internal/testenv/testenv.go#L88)（`WithDataDir`）/ [internal/testenv/testenv.go:94](../../internal/testenv/testenv.go#L94)（`WithCredentialCLI`）/ [internal/testenv/testenv.go:52](../../internal/testenv/testenv.go#L52)（`Clock.Advance` 同步假 CLI 的 now）；测试在 [app/credential_test.go](../../app/credential_test.go)、[app/atomic_test.go](../../app/atomic_test.go)、[api/credential_test.go](../../api/credential_test.go)、[app/device_test.go:234](../../app/device_test.go#L234)。

**修复**：新增 15 个测试函数，覆盖签发（id 与私钥配对）、空串永不签发、管理端签发下发、续期不重复、确认后只留最新那张、吊销清行、无通道的失败、取消上下文、F1 收敛、F2 并发配对、F3 迁移、F7 唯一临时名、管理端签发与 409 映射。私钥里编码了 id，所以「串台」直接被断言，不依赖日志或事件顺序。

**验证**：`go test ./... -count=1` 全绿；测试函数从评审时的 66 个增加到 81 个（另有两个 `TestMain`，不计入）。测试不连种子：CLI 由测试二进制顶替，时钟由 `testenv.Clock` 控制。

### [P3][F7] 发布模板写入的原子性

**位置**：[app/atomic.go:21](../../app/atomic.go#L21)（`stageFile`）、[app/settings.go:149](../../app/settings.go#L149)（`Publish`）。

**修复**：临时文件改用 `os.CreateTemp(dir, "peer.default.toml.*")` 生成唯一名，两个并发发布不会再共用一个 `.tmp`（后写者的内容不会被先提交者的 rename 带走）；模板的 rename 提前到槽位事务之前——rename 失败时一个槽位都还没写，不会出现「返回 500 但槽位已生效」。两处存储没有共同的提交点，残留窗口是槽位事务失败而模板已经换新（代码注释写明了取舍，见文末分歧点 3）。

**验证**：[app/atomic_test.go:12](../../app/atomic_test.go#L12) `TestStageFileUsesUniqueTemporaryNames`：两次 stage 的临时名不同、两次 commit 都成功、最终内容是第二次写入的、目录里没有残留临时文件；`probe_publish_atomicity`（不带 `default_toml` 的路径）仍 `NOT REPRODUCED`。

**已知行为（已确认接受）**：槽位事务失败（罕见路径）时响应 500 但模板已经换新。为它把默认模板也存库、做成跨存储的原子提交不值得。

### [P3][F8] 删除设备的阻塞调用与输出上限

**位置**：[app/device.go:204](../../app/device.go#L204)（`DeleteDevice(ctx, …)`）、[app/credential.go:140](../../app/credential.go#L140)（总时限）、[app/credential.go:97](../../app/credential.go#L97)（`run` 输出上限）、[api/devices.go:219](../../api/devices.go#L219)（传 `r.Context()`）。

**修复**：`DeleteDevice` 接收 `context.Context`，handler 传 `r.Context()`；`RevokeCredential` 给整次撤销 30 秒总上限（单次 CLI 仍是 15 秒，取二者较小值），调用方取消会立刻停下并保留未撤销的行（下一轮可以重试）；`run()` 的 stdout 改成 64KiB 上限的 `boundedBuffer`，子进程写出巨量内容也不会把内存吃掉。`DeleteDevice` 签名变化波及上一轮探针 `probe_legacy_resurrect` 的一处调用，已改成 `context.Background()`，逻辑与结论不变。

**验证**：[app/credential_test.go:130](../../app/credential_test.go#L130) `TestRevokeCredentialHonorsContext`（已取消的 context → 返回错误、行还在）；全量 `go build`/`go vet`/`go test` 通过。没有构造「慢种子 × 多张凭据」的端到端用例（需要假的慢 CLI，未加）。

### [P3][F9] 部署来源的文档与清单矛盾

**位置**：[k8s.mesh-server.yaml:48](../../k8s.mesh-server.yaml#L48)、[DESIGN.md:15](../../DESIGN.md#L15)-[DESIGN.md:17](../../DESIGN.md#L17)、[Dockerfile.runtime:1](../../Dockerfile.runtime#L1)-[Dockerfile.runtime:4](../../Dockerfile.runtime#L4)。

**修复**：删掉清单里「本地构建的镜像（见 Dockerfile.runtime），不从仓库拉取」那行，只留「镜像由 GitHub Actions 构建并推到 GHCR，部署机只负责拉取」；DESIGN `1` 改成「CI 构建推 GHCR、部署机用 `imagePullPolicy: Always` 拉取」；`Dockerfile.runtime` 明确写成离线兜底，并写明它没有 `easytier-cli`（用它时 `ET_CLI` 留空，或自行把 CLI 打进镜像）。

**验证**：人工对照清单的 `image`/`imagePullPolicy` 与 CI workflow（`build-and-push-docker-image.yml` 推日期 tag + `latest`）；没有跑镜像构建。

## 构建与测试（实测）

| 命令 | 结果 |
| --- | --- |
| `gofmt -l .` | 空 |
| `go vet ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `go test ./... -count=1` | exit 0：`api 6.826s`、`app 2.109s`、`core 0.990s`、`db 2.322s`；其余包与 `code-reviews/**` 的探针无测试；81 个 `Test*` |
| `pnpm typecheck`（`frontend/`） | `vue-tsc --noEmit`，exit 0 |

## 探针复跑

| 探针 | 修复前 | 本次 |
| --- | --- | --- |
| `probe_credential_reissue` 第一段 | 6 张（1 初次 + 1 续期 + 4 张报旧 id 时），`REPRODUCED` | 2 张（1 初次 + 1 续期），报旧 id 的 4 轮全 204，`NOT REPRODUCED`（探针已按契约把首心跳改成 `none`、假 CLI 的 expiry 跟随假时钟，见 F1） |
| `probe_credential_reissue` 第二段 | `mismatched_pairs=2`，`REPRODUCED` | `mismatched_pairs=0`、`NOT REPRODUCED: every response paired its secret…`；`answered_with_credential=8` 是「设备没有任何凭据记录」时 8 条并发 `none` 各签一张（取舍 3） |
| `probe_credential_orphan` | 撤销/删除均 204，行留下 | 均 `409 credential_channel_unconfigured`，行留下（刻意） |
| `probe_stored_hash` | `REPRODUCED`（stale） | `NOT REPRODUCED`（存量摘要已重算） |
| 上一轮 6 个探针 | — | 原样重跑全部 `NOT REPRODUCED` / `Allow` 正确（`probe_legacy_resurrect` 只改了 `DeleteDevice` 调用签名一处） |

## 未自动验证 / 留给下一轮

- 未连真实种子（ali-server / tencent-server / ali-server-zyf）：签发与撤销仍由假 CLI 覆盖；`ET_RPC_ADDR` 的转发链路、镜像里 `easytier-cli` 与种子 core 的次版本一致性都未实测。
- 未跑 `just check`（本机没有 `just`）、`pnpm build`（会重写 `frontend/dist`，属构建资产）、`docker/podman` 与 CI。
- F7 的并发发布只测到 `stageFile` 的唯一临时名，没有端到端并发发布；F8 没有慢种子用例。
- 根目录 `CONTRACT.md` 1.5 的凭据段已改写为当前规则（含 `none` 冷却、报旧 id 补签、空串不签发；[CONTRACT.md:169](../../../CONTRACT.md#L169)-[CONTRACT.md:175](../../../CONTRACT.md#L175)），但该文件不在 `server/` 仓库里（`mesh-app` 根不是 git 仓库），所以不在本次提交中；`client/` 未动。

## 本轮确认的取舍

1. **`none` 的签发冷却**：同一设备距最近一条凭据的 `created_at` 不足 5 分钟不再签发（实现见 F1 的冷却段）。它压的是稳态异常——并发心跳、本地文件一直写不进去时每 30 秒一轮的补签。
2. **报旧 id 冷却外补签**：管理端 `POST /api/devices/{id}/credential` 签的凭据只能靠心跳补签落到设备上；代价是设备卡在旧 id 上（core 起不来）时每 5 分钟多一张废凭据，由冷却压住。这是冷却外的有界补签，不是回到「每轮心跳都签」。
3. **「无记录 + 并发首轮各签一张」接受现状**：不做原子占位（按设备串行签发或唯一约束）。正常客户端由 `instance.lock` 保证单实例串行心跳，并发只出现在人为压测或异常；多签出来的是没被确认过的废凭据，不影响设备、不吊销任何东西，只占几行表。`probe_credential_reissue` 第二段因此仍显示 8 条各签一张，那不是 F2 的串台，判据是 `mismatched_pairs=0`。
4. **空串永不签发**：契约与 `DESIGN.md` 优先于探针。`reportedID == ""` 的判断排在读凭据表之前，正式节点与旧客户端连第一张都不会白拿；评审探针 FIRST 段原来用空串造设备，已按契约改成 `none`（改的是我们自己的验证脚本，不动生产代码）。
5. **根目录 `DESIGN.md`**：lead 已在评审后同步「没有私钥可重发」的理由；本轮把签发收成三种情况（没有凭据、临近到期、报旧 id 且冷却外），补了 5 分钟冷却一句，管理节点那句回到「既不确认也不签发」。
6. **F7 的残留窗口**：接受现状，记录为已知行为（见 F7）：槽位事务失败时响应 500 但模板已生效。跨存储的原子提交（把模板也存库）不做。
7. **无 `ET_CLI` 时删不掉设备**：接受现状，不做「先删本地记录 + 后台重试」（见 F4）：凭据还在种子上就是还能连，先删记录只会让控制面失忆。