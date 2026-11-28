# 控制面修复回应

- 对应审查：[REVIEW.md](REVIEW.md)（提交 `17680a7`，2026-11-28T14:20:46+08:00）
- 回应时间：2026-11-28T15:00:00+08:00
- 修复提交：见本轮 `git log -1`

## 逐项

### F1 nonce 缓存饱和被表达成认证失败（P1）——已修

`NonceCache.Claim` 返回三态 `NonceAccepted` / `NonceReplay` / `NonceExhausted`：重放仍是 401，容量耗尽回 `503` `{"code":"busy"}` 与 `Retry-After: 1`，且不驱逐窗口内的条目。并发闸门满载共用同一个 `writeBusy`，一条事实一份实现。注册的签名支路同一判据：签名可用但 nonce 登记不下且请求没带令牌时回 503，带令牌则照常走令牌证明（`core/nonce.go`、`api/device_api.go`、`api/api.go`）。

验证：`TestNonceCacheReportsExhaustionWithoutEvicting`、`TestNonceCacheExhaustionIsRetryable`（心跳 503 + `Retry-After` + `busy`；窗口内 nonce 仍是 401；注册签名支路 503；带令牌注册 200）。客户端侧无需改动：`401` 才计入认证失败，5xx 走退避（客户端本轮 F1 的跨端项已固化该边界）。

### F2 认证主体只绑可复用标识（P2）——已修

认证主体 `DeviceAuth` 带上验签实际使用的那把公钥；应用层锁内复核与数据库条件写用同一判据：`AcceptRegistration` 在锁内核对 `device_id` 与「验签那把公钥仍在位」，不符回 401 且不写库；心跳的 `Observe` 在 `WHERE` 里加 `pubkey = ?`，失配与换绑同一条 `ErrIdentityChanged`（`api/device_api.go`、`app/device.go`、`app/heartbeat.go`、`db/device.go`）。

验证：`TestHeartbeatRefusesTheSameDeviceIDAfterItsKeyWasRotated`、`TestAcceptRegistrationRejectsAStaleSignatureProof`、`TestObserveMatchesTheAuthenticatedKey`。

### F3 签发结果未完整校验（P2）——已修

`generate` 校验整份回答：`id` 必须等于意图行 `id`、私钥必须是 base64 的 32 字节（按规范重编码后交出）、有效期必须落在 `now + ttl ± ClockSkew`。校验后的单一结果用于完成意图行与构造响应；不符走原补偿（撤销意图 id 与形状合法的回答 id、删意图行），绝不留下三份不同源的凭据。`FinishCredential` 只补 `expires_at = 0` 的意图行（`app/credential.go`、`db/credential.go`）。

验证：`TestIssueCredentialRejectsAnAnswerForAnotherID`、`TestIssueCredentialRejectsAMalformedSecret`、`TestIssueCredentialRejectsAnExpiryOutsideTheRequest`、`TestFinishCredentialFinishesAnIntentRowOnce`；假 CLI 的密钥改为由 id 决定，断言可直接比对同源性。

### F4 设备锁等待没有取消语义（P2）——已修

`keyedLock` 换成「一格信号量 + 引用计数」，`lock(ctx, key)` 用 `select` 等 `ctx.Done()`：排队中被取消或超时的请求当场放掉引用、返回 `ctx.Err()`；拿到锁后再查一次 context，锁可能正是在请求失效之后才空出来的。两处检查合起来保证「释放引用」与「不进入写副作用路径」是同一件事。保留按设备串行、条目最后引用回收与全局 HTTP 闸门；全入口（心跳、注册、确认、签发、撤销、删除、换绑）都改带 ctx（`app/locks.go` 及各调用点）。

验证：`TestKeyedLockWaiterGivesUpWhenItsRequestEnds`、`TestKeyedLockStaysUsableAfterACancelledWaiter`、`TestHeartbeatDoesNotWriteWhenItsRequestEndsWhileQueued`、`TestCancelledQueuedHeartbeatReleasesItsGate`。

### F5 发布端与安装端的归档合法域不一致（P2）——已修

预算统一为契约值：条目 1024、解压后 256 MiB（原 4096 / 512 MiB），清单 64 KiB、包 200 MiB 不变；补上启动器的「成员必须是普通文件」判据（`tar.Typeflag` 非普通文件即拒绝）。控制面因此在写入发布记录之前就拒绝目标端安装器会拒绝的包，MESH3/MESH4 与文件集合语义一行未动，没有第三套包格式（`app/settings.go`）。契约同步见 `docs/contract/updates.md`「启动器侧的预算」。

验证：`TestPackageMembersMustBeRegularFiles`、`TestPackageDeclaredSizeIsBoundedByTheLauncherBudget`。

### F6 新增 CHECK 约束未进入结构验证（P2）——已修

`tableEntries` 增加 CHECK 条目：从 `sqlite_master` 的建表文本取 `CHECK (...)` 片段，规范化后按表达式计数比较；括号配平扫描跳过单引号字面量，注释先剥掉；引号外大小写与标点两侧空白视为同一份，字面量内原样比较。参考库仍是同一份 DDL 建出来的，单一事实来源不变，没有引入 SQL 解析框架（`db/db.go`）。

验证：`TestOpenRejectsSchemaDrift` 的两个新用例（删掉 slot 约束、删掉启动器行约束，都必须启动失败）、`TestOpenAcceptsTheSameChecksWrittenDifferently`（大小写与空白重写仍通过，注释里的 check 字样不参与比较）。

### F7 `DecodeJSON` 只看第一个 JSON 值（P3）——已修

成功解出业务值后再读一个记号，只有 `io.EOF` 才算请求体结束：第二个值、尾随字符与越过上限的读取都到不了 EOF，统一 `400 invalid_request`。因为读到了结尾，`MaxBodyBytes` 才真正作用于完整请求体（`httpx/response.go`）。

验证：`TestDecodeJSONAcceptsOneCompleteValue`、`TestDecodeJSONRejectsContentAfterTheValue`、`TestDecodeJSONBoundsTheWholeBody`。

### F8 响应写入没有独立期限（P3）——已修

`httpx.WithWriteDeadline` 在两个面各包一层：期限在响应的第一个字节处开始（`WriteHeader`/`Write` 首次调用），30 秒到即断。它不覆盖后端时间——发布下载的 200 MiB 抓取与凭据签发的 RPC 各有自己的预算，用一个覆盖整条业务链的截止时间会把它们拒掉而客户端看不出原因。不支持期限的 `ResponseWriter` 只记 Debug 日志，响应照旧（`httpx/response.go`、`main.go`）。

验证：`TestResponseWriteDeadlineStartsWhenTheResponseDoes`、`TestResponseWithoutABodyGetsNoWriteDeadline`。

## 已知安全事项与接受项

| 编号 | 本轮回应 |
| --- | --- |
| S5 | 按设备/按 IP 的限速归反向代理，控制面明确不做——这条已写进 `docs/security.md`「已接受的残余风险」。本轮的 F1、F4 与限速无关：它们是错误语义与取消语义，已按上面两项修掉。 |
| S6 | 保留既有发布预算；与 F5 一起统一到安装端的数字（1024 / 256 MiB），压缩字节的大小检查仍在整包读完之后，需更早失败得加 `Content-Length` 预检（未做，留待排期）。 |
| S7 | 未动：镜像与 CLI 的 digest 钉死、部署记录 digest 仍待排期。 |
| S8 | 审查指出的「PENDING 描述已过期」成立：`prepareClientSlot` 早已调用 `ValidateManifest`、`VerifyManifest` 并比对归档摘要，因此该行已从 `PENDING.md` 删除，不再作为待办保留。 |

## 验证

| 命令 | 结果 |
| --- | --- |
| `gofmt -l .` | 空 |
| `go vet ./...` | 通过（含 `code-reviews/` 下的探针：`probe_nonce_window` 随 `Claim` 的三态改了一行比较，其它探针不受影响） |
| `go test ./... -count=1` | 全绿：root、api、app、builtin、core、db、httpx、signing、text |
| `go test -race ./app ./api -run 'TestKeyedLock|TestHeartbeatDoesNotWrite|TestCancelledQueuedHeartbeat|TestNonceCacheExhaustion'` | 通过 |

## 未自动验证与剩余风险

1. 响应写期限没有做真实 TCP 停顿验证：需要客户端停读把套接字缓冲写满并等满 30 秒，本机回环还有 10013 噪声；测试断言的是 `ResponseController` 那层契约与起算点，剩余由部署侧核对。
2. 签发有效期的上界依赖控制面与种子时钟相差不超过 `ClockSkew`（300 秒）：线上二者同机，若将来分开且漂移超限，签发会显式失败（fail-closed）而不是记下一张错期凭据。
3. 取消后的可观测性：排队中被取消的心跳会打一条 `context canceled` 并按 500 记访问日志（连接已断，写不出去）。语义正确但不区分「服务端错误」与「调用方放弃」；是否降噪留给下一轮定。
4. 反向代理的读写超时与限速配置未核对，本轮没有接触线上反代、种子或生产库。

## 新发现

CHECK 比较是文本规范化比较：如果将来有人用触发器或断言承载同一语义，这套验证看不到。目前 schema 只用 CHECK，暂不需要扩展。
