# 服务端审查回应

- 对应审查：`code-reviews/2026-11-28T04-52-21+0800_6878f1/REVIEW.md`（审查提交 `6878f1e`）
- 回应时间：2026-11-28T05:40:00+08:00
- 范围：F1、F2、F3 与 F5 的两条实现项已处理；F4 与 F5 的协议项按指示留待后续。

## 结论

| 编号 | 级别 | 结论 | 一句话 |
| --- | --- | --- | --- |
| F1 | P2 | 已修复 | `app` 接手完整的注册与心跳入口，一次条件写入产生快照，响应与凭据决定都从这一行来 |
| F2 | P2 | 已修复 | 模板改由 `PUT /api/admin/template` 单独保存，发布请求只带工件，两者不再有共同提交点 |
| F3 | P2 | 已修复 | 声明之外的普通索引放行，显式 UNIQUE 索引按结构差异报错 |
| F4 | P3 | 未修复 | `release` 表按 `(platform, slot)` 拆行需要数据契约与线上迁移，待裁决 |
| F5 | P2 | 部分修复 | 两条实现项已做（结构比对规范化、CLI 预算对齐）；`appUpdateKnown`/`unchanged` 属跨两端协议，另开一轮 |

## 逐项回应

### [P2][F1] 设备协议业务动作横跨 API、应用层和数据库多个一致性边界

- 做法：按「一层只做一件事」切开。`api` 只解码、认证（值域、时间窗、验签、nonce）、错误映射与编码；`app` 提供两个完整入口 `Heartbeat(ctx, DeviceAuth, HeartbeatObservation) → HeartbeatResult` 与 `AcceptRegistration`，各自在设备锁内完成「复核当前绑定 → 一次条件写入 → 按写出来的那一行规划响应」。
- 一次心跳只有一次写：`db.Observe` 用 `WHERE public_id = ? AND device_id = ?` 的条件 UPDATE 写观测值（`COALESCE(NULLIF(?, ''), last_ip)` 表达「空 IP 不改」），再用同一事务取出那一行作为快照；配置下发、`appUpdate`、启动器、凭据步骤全由这一行决定。换绑、删除或另一次心跳只能落在它之前或之后，因此不存在「观测已写、响应仍来自旧绑定」的中间态。
- 已绑定槽位的注册合并为一个 `database.Update`（复核绑定 → 验令牌 → 验公钥 → 清令牌 → 记最近在线），被拒时整行不变。未绑定仍走 `BindDevice`，两者对应两种真实状态，合并需要一个「走哪个入口」的标志位，反而多一个分支。
- 令牌校验顺序改为先验令牌、再判公钥：否则任何知道 `deviceId` 的人用「假令牌 + 猜的公钥」就能从 `409` 探出槽位是否已绑定。
- 删掉的方法：`db.Touch`、`db.RecordHeartbeat`、`app.Touch`、`app.RecordHeartbeat`、`app.ConsumeTokenIfCurrent`、`app.CredentialStep`（导出）、`api.slotTokenValid`、`api.registerProofValid`。
- 位置：`app/heartbeat.go`、`app/device.go`、`db/device.go`、`api/device_api.go`、`app/credential.go`。
- 验证：`TestHeartbeatAnswersFromTheRowItWrote`、`TestHeartbeatWritesNothingAfterARebind`、`TestHeartbeatCredentialStepUsesTheSameSnapshot`、`TestAcceptRegistrationSpendsTheTokenOnce`、`TestAcceptRegistrationRejectsAnotherIdentity`、`TestAcceptRegistrationKeepsTheTokenOnAPubkeyMismatch`、`TestAcceptRegistrationChecksTheProofBeforeThePubkey`；并发与既有回归 `TestConcurrentIssuanceKeepsCredentialPairs`、`TestReissuedTokenReenrollsDevice`、`TestHeartbeatIssuesCredentialOnlyWhenNeeded`、`TestNoneCooldown`、`TestRenewalIsNotRepeated`、`TestAdminIssuedCredentialReachesDevice` 未改一行仍通过。

### [P2][F2] 默认模板与发布槽位共用请求，但缺少共同的提交边界

- 做法：模板迁到 `PUT /api/admin/template`（响应即保存之后生效的那一份，空白保存等于删除文件、回内置模板），发布请求只含工件，`PublishInput` 去掉 `DefaultTOML` 后只剩三个槽位字段；`Publish` 里那段「先存模板再写槽位」的顺序协调与注释一并删除。
- 两个槽位仍在同一笔 SQLite 事务里提交（`db.PublishSlots` 未改）；模板与槽位各自一个存储、一个写入口，因此不需要任何共同提交点，也不存在「模板已生效而槽位失败」的窗口。
- 同时删掉 `GET /api/admin/releases` 的 `defaultToml`：模板已有独立读入口，再在发布响应里带一份等于同一事实两个来源，前端已无消费方。
- 位置：`app/settings.go`、`api/settings.go`、`api/devices.go`、`frontend/src/api.ts`、`frontend/src/components/ConfigView.vue`、`frontend/src/App.vue`。
- 验证：`TestTemplateAndPublishHaveSeparateOutcomes`、`TestSetTemplateBlankRestoresBuiltin`、`TestTemplateSaveHasItsOwnOutcome`、`TestPublishRejectsBothSlotsTogether`、`TestReleaseRoundTrip`。

### [P2][F3] 附加命名 UNIQUE 索引没有按其对约束的影响处理

- 做法：`tableEntries` 把列、命名索引与隐式唯一约束都规范成条目，判据改为「声明之外且唯一」才保留为差异；`diffTable` 报 `table %s has an undeclared unique index %s`。普通非唯一索引仍然放行——它只影响查询。
- 位置：`db/db.go`。
- 验证：`TestOpenRejectsAnExtraUniqueIndex`（附加 UNIQUE 启动失败并点名索引）、`TestOpenRejectsAnExtraPlainIndex`（普通索引放行）、既有 `TestOpenRejectsSchemaDrift` 与 `TestOpenRejectsAnIndexThatLostItsConstraint` 继续通过。
- 线上库：只读核对过，命名索引仅 `credential_device`（非唯一）与 `device_device_id`（唯一），都在声明内，因此这条新失败面在当前线上不存在。

### [P2][F5] 增量更新协议及部分辅助机制可进一步简化

本轮做完两条实现项，协议项留下一轮。

| 位置 | 本轮做法 |
| --- | --- |
| `db/db.go` 的结构比对 | 每侧一次规范化 + 一次条目 diff + 一次属性差，删掉 `compareTable`、`compareColumns`、`compareIndexes`、`splitIndexes`、`uniqueKey` 等 8 个逐层遍历函数；差异消息仍点名表、对象与属性 |
| `app/credential.go` 的 CLI 预算 | 选择「各自有界」而不是共用记账：注释与实现统一为「每条流各自 64 KiB」。共用一份预算要引入跨流状态并让截断归因变复杂，只省 64 KiB |

- 验证：`TestOpenRejectsDatabaseMissingAColumn`、`TestOpenRejectsLegacyDatabaseWithoutDroppingItsRemnants`、`TestCLIOutputBudgetIsPerStream`（fakecli 先写满 stderr 再写满 stdout，两条各 64 KiB 成功、多 1 字节报超限——共用预算的实现在这条用例上就会失败）、`TestCLIFailureOutputIsBounded`。
- **未做**：删除 `appUpdateKnown` 与 `unchanged`。它同时改服务端字段、客户端字段与两边契约，客户端那一半在同一轮里并行处理，按指示留到跨两端的单独一轮。

## 裁决答复

| 议题 | 决定 |
| --- | --- |
| `GET /api/admin/releases` 去掉 `defaultToml` | 接受。模板有独立入口，发布响应再带一份是同一事实两个来源。 |
| 发布请求里的 `defaultToml` 静默忽略还是显式拒绝 | 都不取：改为解码器统一拒绝。`httpx.DecodeJSON` 打开 `DisallowUnknownFields`，未声明字段与解析失败一律 `400 invalid_request`；`releaseInput.AppVersion` 这个「只为拒绝而存在」的字段及其分支随之删除，`defaultToml`、`POST /api/admin/devices` 的 `autoUpdate` 由同一条规则覆盖。 |
| 认证后被换绑/删除改回 `401` | 接受。旧实现带认证时的快照回 `200/204`，那条路径本身就是不一致的来源。契约已写明。 |
| 管理端 `UpdateDevice` 是否进设备锁 | 不进。它是单语句事务写，心跳的「写观测值 + 取快照」在同一事务内完成，两者并发只会晚一拍收敛，拼不出「观测值与配置正文不同源」的行；进锁会让一次配置保存排在 30 秒级的撤销之后。该不变量已写进 `server/DESIGN.md`。 |
| 线上残留显式 UNIQUE 索引 | 已核对，不存在（见 F3 末条）。 |

## 净变化

| 项 | 之前 | 之后 |
| --- | --- | --- |
| 一次心跳对设备行的写次数 | 2（`deviceAuth` 里的 `Touch` + `RecordHeartbeat`） | 1（`app.Heartbeat` 内 `db.Observe`），`api` 层 0 写 |
| 已绑定注册的设备行写次数 | 2 | 1（`AcceptRegistration`） |
| `app` 导出的设备协议入口 | 4 | 2 |
| 心跳响应规划的位置 | 跨 `api` 与 `app` | 全在 `app` |
| `verifySchema` 的遍历层 | 2 次 PRAGMA + 逐列 + 逐索引 + 约束计数，跨 8 个函数、3 种形状 | 每侧一次规范化 + 一次条目 diff，3 个函数、1 种形状 |
| `PublishInput` 字段 | 4 | 3 |
| 模板与发布的共同提交点 | 1（带承认的窗口） | 0 |
| 请求体字段 | — | −2（`releaseInput.appVersion`、`createDeviceRequest.autoUpdate`） |
| 请求体类型 | — | −1（`createDeviceRequest` 整个删除） |
| 逐字段的显式拒绝分支 | 2 | 0（统一到解码器） |
| 解码错误的 code 写入点 | 9 处，`bad_request` 与 `invalid_request` 混用 | 1 处（`httpx.DecodeJSON`） |
| text 常量 / 语言条目 | — | −3 / −6 |

## 文档

| 文件 | 改了什么 |
| --- | --- |
| `docs/contract/device-api.md` | 心跳的一致性边界：观测、响应与凭据决定来自同一行；换绑或删除发生在处理期间回 `401` |
| `docs/contract/admin-api.md` | 新增「通用约定」：未声明字段与请求体解析失败一律 `400 invalid_request`，规则由解码器统一执行；`PUT /api/admin/template`；releases 响应只含 `releases` |
| `docs/contract/updates.md` | 发布请求只含工件、模板独立路由；原子性改为「两个槽位同一事务」；未声明字段（如 `appVersion`、`defaultToml`）统一 `400 invalid_request` |
| `server/DESIGN.md` | 设备协议分层、管理端改配置为何不进设备锁、结构比对的判断依据 |

## 验证

| 命令 | 结果 |
| --- | --- |
| `gofmt -l .` | 无输出 |
| `go vet ./...` | 通过 |
| `go test ./... -count=1` | 8 个测试包全部通过（`mesh-server`、`api`、`app`、`builtin`、`core`、`db`、`signing`、`text`） |
| `pnpm typecheck` / `pnpm test` / `pnpm build` | 通过；前端单测 40 例 |

未自动验证：真实种子 RPC、容器部署、生产库迁移演练、浏览器交互。

## 未修复项

| 编号 | 事项 | 原因 |
| --- | --- | --- |
| F4 | `release` 表按 `(platform, slot)` 拆行 | 需要先改数据契约并按既有迁移策略改线上库，本轮不动，等裁决 |
| F5 协议项 | 删除 `appUpdateKnown` 与 `unchanged` | 同时改服务端、客户端与两处契约，客户端那半在同一轮并行处理，留到跨两端的单独一轮 |
