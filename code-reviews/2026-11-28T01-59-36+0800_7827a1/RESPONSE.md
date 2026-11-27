# 服务端审查回应

- 对应审查：`code-reviews/2026-11-28T01-59-36+0800_7827a1/REVIEW.md`（审查提交 `7827a1a`）
- 回应时间：2026-11-28T04:05:00+08:00
- 范围：9 项全部处理；F1、F2 在上一提交 `f268bb0` 里已完成，本轮补 F3–F9，并把 F5 的启动校验放到线上库上核过。

## 结论

| 编号 | 级别 | 结论 | 一句话 |
| --- | --- | --- | --- |
| F1 | P0 | 已修复（`f268bb0`） | 凭据生命周期收敛为设备级串行协议：一把按设备的锁、签发先落意图行、失败补偿撤销 |
| F2 | P0 | 已修复（`f268bb0`） | 一次性令牌的核验与消耗进了同一个 `database.Update`；换绑先撤销旧凭据 |
| F3 | P2 | 已修复 | 设备字段值域集中一处、时间窗改区间比较、nonce 缓存有容量且饱和时拒绝而不驱逐 |
| F4 | P2 | 已修复 | 发布时验内层清单签名与整个归档的文件集合，控制面不再接受启动器必然拒绝的工件 |
| F5 | P2 | 已修复 | 启动校验比对列（类型/非空/默认值/主键）与索引（唯一性/部分/列序），不再只看列名 |
| F6 | P2 | 已修复 | stdout/stderr 共用一份预算并记截断，`WaitDelay` 给管道收尾设上限 |
| F7 | P3 | 已修复 | 身份解析一次成型，解析失败返回错误与零值身份，管理面带 `configError` |
| F8 | P3 | 已修复 | 列表请求有资源代次，只有最新有效结果写状态 |
| F9 | P3 | 已修复 | 注册令牌字符集统一为 `0-9`/`a-f` |

## 逐项回应

### [P0][F1] 凭据生命周期缺少设备级一致性边界

- 做法：新增 `keyedLock`（`app/locks.go`，按设备 public id 分锁、引用计数归零即回收），`IssueCredential`/`RevokeCredential`/`DeleteDevice`/`BindDevice` 与心跳的 `CredentialStep` 全部在同一把锁内完成「读身份 → 决策 → 外部签发 → 落库」。签发先写意图行（`expires_at = 0`）再调种子，失败则按 id 补偿撤销，成功再补 `expires_at`；`LatestCredential` 只认 `expires_at > 0`，意图行因此不会被当成「已签发」。身份代次不一致返回 `ErrIdentityChanged`，由调用方放弃这次签发。
- 位置：`app/credential.go`、`app/locks.go`、`app/device.go`、`db/credential.go`、`api/device_api.go`。
- 验证：`TestDeleteSerialisesWithAnIssuance`、`TestFailedIssuanceLeavesNoIntentRow`、`TestRebindRevokesTheOldCredentials`、`TestKeyedLockSerialisesOneKeyOnly`、`TestKeyedLockForgetsReleasedKeys`、`TestIntentRowIsNotIssued`、`TestFinishCredentialStampsTheExpiry`、`TestFinishCredentialRejectsAMissingRow`。
- 线上验证：部署 `f268bb0` 后对 wsl 连续两次 `DELETE /api/admin/devices/d_173859/credential` 都返回 `204`（第二次 0 ms：没有行可撤也不算失败），45 秒内心跳按新逻辑重签，`credential.json` 落盘、隧道与 ping 全程未断。

### [P0][F2] 已绑定设备一次性令牌的核验与消耗非原子

- 做法：`ConsumeTokenIfCurrent(publicID, token)` 在同一个 `database.Update` 事务里「检查 token 与到期时间 + 清空 + 返回是否消费成功」，注册的已绑定分支只按它的结果决定是否返回配置（消费失败 → `401`，持久化失败 → `500`，不再「只记日志」）。签名认证分支不动。`BindDevice` 先 `revokeDeviceLocked` 再更新绑定，换绑后旧身份的凭据不会残留。
- 位置：`app/device.go`、`api/device_api.go`。
- 验证：`TestConsumeTokenIsAtomic`、`TestRebindRevokesTheOldCredentials`。

### [P2][F3] 签名时间窗与设备字段缺乏完整合法域和资源约束

- 做法：新增 `core.DeviceRequestFields` 一处声明值域（`deviceId` 1–128、`timestamp` 1–19 位十进制、`nonce` 1–64、`platform` ≤32、`configHash`/`appVersion` ≤64、`signature` 1–128、`credentialId`/`appUpdateKnown` ≤64，全部只许可打印 ASCII），在查库、验签、写状态之前拒绝；时间窗改成 `signedAt ∈ [now-300s, now+300s]` 的区间比较，不再用 `Duration` 差的绝对值；nonce 缓存有容量上限 `8192`，饱和时拒绝新 nonce 而不驱逐仍在窗口内的条目（驱逐等于让用过的签名复活），与重放同为 `401`。
- 位置：`core/device.go`、`core/signing.go`、`core/nonce.go`、`api/device_api.go`。
- 验证：`TestValidateDeviceFields`、`TestCheckTimestamp`（窗口两端 ±1s、`MaxInt64`/`MinInt64`、超出 int64）、`TestNonceCache`、`TestDeviceFieldDomainIsRejected`（带换行或超长字段的请求即使签名正确也 `401`，换行 `deviceId` 的注册 `400`）。

### [P2][F4] 发布路径只能证明外层签名和部分清单形状

- 做法：`signing.VerifyManifest` 与客户端启动器共用同一段原文与同一把公钥；发布时在「合法域 + slot/platform 一致」之后验内层 `MESH4` 签名，缺失或与内容不符即 `400` 且不写槽位。归档侧改为扫完整个成员表：逐成员求摘要（仍在同一个 tee 上算整包摘要，外层 `MESH3` 语义不变），拒绝重复成员、`./`/`..` 未规范化名、清单列了没出现、出现却没列、摘要不符；截断归档在扫到结尾时失败。
- 位置：`signing/signing.go`、`app/settings.go`、`internal/testenv`（`SignManifest` 夹具）。
- 验证：`TestVerifyManifestBindsTheFileSet`、`TestReadPackageManifestVerifiesTheFileSet`（缺文件、摘要不符、多出文件、`./` 名、同名成员、清单重复、截断归档）、`TestPublishRequiresTheManifestSignature`、`TestPublishRequiresASignature`。
- 取舍：`docs/contract/updates.md` 原本写「控制面只验包签名」，本轮改为验 `MESH3` + `MESH4` + 文件集合；历史包若清单未签名，现在发布会 `400`。这与启动器行为一致（那种包本来就装不上）。

### [P2][F5] 已有数据库的列名校验不足以确认数据契约

- 做法：仍以内存参考库为唯一事实来源，比对升级为列（名、类型、非空、默认值、主键）与索引（声明的索引按名字比唯一性/部分标志/列序；`UNIQUE` 生成的隐式索引按形状多重集比）。默认值统一大小写、去空白与字面量外括号后比较；表缺失单独报 `table X is missing`。
- 取舍：声明之外的**附加命名索引**不报错（只影响存储，不改变表接受什么）；额外 `UNIQUE` 约束仍然报错（会改变写入行为）；部分索引的 `WHERE` 谓词文本不逐字比较——`!=` 与 `<>` 之类的等价写法会让手工迁移误判。
- 位置：`db/db.go`。
- 验证：`TestOpenRejectsSchemaDrift`、`TestOpenRejectsAnIndexThatLostItsConstraint`、`TestOpenRejectsDatabaseMissingAColumn`。写这个检查时踩到并修掉一个真问题：单连接下嵌套 `PRAGMA` 查询会死锁，改成先读完 `index_list` 再逐项查 `index_info`。
- **线上库已核对**：清理完的库上 `device.public_id`、`release.platform`、`credential.credential_id` 都带 `UNIQUE`，`device(device_id)` 是 `WHERE device_id <> ''` 的部分唯一索引，`credential(device_public_id)` 是普通索引，与 `db/db.go` 的声明一致，因此启动不会被这次收紧拦下。

### [P2][F6] 签发子进程的失败输出与清理时间没有有效上限

- 做法：`boundedBuffer` 记录 `truncated`，stdout 与 stderr 共用同一份 `64 KiB` 预算，超限用「（输出已截断）」收尾；stdout 被剪断时返回稳定错误而不是交给 JSON 解析；`cmd.WaitDelay = 2s` 给「主进程已退出但管道仍被其子进程持有」设上限——此前 `Wait` 会跟着那个孙进程的寿命走。
- 位置：`app/credential.go`、`internal/fakecli`（`MESH_TEST_CLI_STDERR_BYTES`、`MESH_TEST_CLI_HOLD_MS`）。
- 验证：`TestCLIFailureOutputIsBounded`（1 MiB stderr 只留预算内一段并报截断）、`TestCLILeavesNoPipeToWaitFor`（孙进程要活 6 秒，签发在 2 秒管道时限内返回失败）。

### [P3][F7] 配置身份分类解析重复，解析失败映射为正常身份

- 做法：`ConfigFactsOf(configTOML) (ConfigFacts, error)` 一次 decode 同时得出身份/地址/DHCP，解析失败返回错误与零值事实（`Identity == ""`），签发路径与显示路径用同一判据。管理面每行带 `configError`（`omitempty`），坏配置只影响自己那一行；前端 `identity` 放宽为 `NodeIdentity | ""`，三态文案、列表「配置错误」标记、详情页展示原因并禁用「轮换凭据」。
- 位置：`app/config.go`、`app/credential.go`、`api/devices.go`、`frontend/src/format.ts`。
- 验证：`TestConfigFactsOf`、`TestUnparsableConfigIsReportedNotClassified`、前端 `format.test.ts`。

### [P3][F8] 同一会话中旧设备列表响应可覆盖新结果

- 做法：抽出 `frontend/src/sequence.ts`（`next`/`current`/`stale`），会话代次改用它，并新增设备列表的资源代次：`loadDevices` 一次领号，落地条件是「会话仍是最新且此后没有更新的列表请求」，设备与分组两张表同时丢弃或同时更新。轮询不串行（串行挡不住换会话），只让最新有效结果写状态。
- 位置：`frontend/src/sequence.ts`、`frontend/src/App.vue`。
- 验证：`sequence.test.ts`（逆序完成只有后发的落地；换会话后旧会话在途请求全部作废；`current` 不推进代次）。未引入 `@vue/test-utils`（不新增依赖），`App.vue` 的接线由 `vue-tsc` + `vite build` 覆盖。

### [P3][F9] 注册令牌的声明、生成及接受合法域不一致

- 做法：判据由 `a-z` 收紧为 `0-9`/`a-f`（64 字符即 32 字节小写十六进制），与注释、生成端（`core.RandomHex`）和自检一致；无需数据迁移。
- 位置：`app/device.go`、`docs/contract/device-api.md`。
- 验证：`TestValidTokenRequiresSixtyFourLowercaseHex`、`TestIssuedTokenHasTheContractShape`（`9`/`f` 边界接受，`g`/`z` 出现在首尾被拒）。

## 需要裁决的点的答复

| 议题 | 决定 |
| --- | --- |
| F5 对线上库是否放行 | 已放行。线上库的约束与索引逐条核过，与声明一致（见 F5 末条）。 |
| F5 部分索引的 `WHERE` 谓词不比较 | 接受。逐字比 `sqlite_master.sql` 会把等价写法判成漂移。 |
| F4 收紧后历史包无法再发布 | 接受。历史包本来就装不上；需要重发就先 `signpkg -manifest` 重签。 |
| F3 注册只带部分签名三件套现在 `400` | 接受。客户端实际行为是「要么都不带、要么都带」。 |
| F3 nonce 缓存键不掺 `deviceId` | 保持全局 nonce。改成 `deviceId + nonce` 等于放宽「同一签名不能重放两次」的语义，不是收紧。 |
| `credentialId` 是否纳入签名原文 | 本轮不做。它决定的是「确认与签发的时机」，不改变验签结果；传输完整性由 TLS 承担。契约措辞已改成与真实作用一致。 |

## 文档

| 文件 | 改了什么 |
| --- | --- |
| `docs/contract/device-api.md` | 设备请求字段合法域表、校验顺序里的第 0 步、时间窗区间比较、nonce 缓存容量与饱和策略、`credentialId` 的作用表述、注册令牌字符集 `0-9`/`a-f` |
| `docs/contract/updates.md` | 发布验收改为 `MESH3` + `MESH4` + 文件集合、拒绝清单补三条、时序图同步 |
| `docs/contract/admin-api.md` | 设备 payload 的 `identity` 可为空串、新增 `configError` |
| `docs/contract/server.md` | 启动校验的范围（列与索引） |
| `server/builtin/peer.default.toml` | `[flags] dev_name = "mesh0"`：新建设备直接拿固定网卡名 |

## 验证

| 命令 | 结果 |
| --- | --- |
| `gofmt -l .` | 无输出 |
| `go vet ./...` | 通过 |
| `go test ./... -count=1` | 8 个测试包全部通过（`mesh-server`、`api`、`app`、`builtin`、`core`、`db`、`signing`、`text`） |
| `pnpm typecheck` / `pnpm test` / `pnpm build` | 通过；前端单测 40 例 |

未自动验证：真实种子与设备上的凭据撤销传播、容器网络与反代、浏览器交互、生产库的备份/恢复演练。

## 未修复项

无。
