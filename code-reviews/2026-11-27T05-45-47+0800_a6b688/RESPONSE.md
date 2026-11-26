# mesh-server 回应（a6b6880，评审 2026-11-27T05-45-47+0800_a6b688）

- **对应评审**：[`code-reviews/2026-11-27T05-45-47+0800_a6b688/REVIEW.md`](REVIEW.md)（基线 `a6b6880`）
- **回应时间**：2026-11-27 06:12 +08:00
- **结论**：F1–F9 全部修；待确认 7 条中 U1、U3、U4、U6、U7 已处理，U2、U5 保留并说明理由。U1 的一次性迁移未动。

## 逐条回应

### [P2][F1] 绑定槽位带无效签名就拒绝有效令牌 —— 已修

改 [api/device_api.go](../api/device_api.go)：`registerProofValid` 改成返回 `(proved, byToken)`，签名这一支失败（时间窗、签名不匹配、nonce 重复）后继续走 `slotTokenValid`，与契约「出示下列任一凭据」一致。两个分支的先后顺序仍是先验签：签名可用时不需要读令牌。

验证：新增 `TestBoundSlotAcceptsValidTokenDespiteBrokenSignature`（[api/regress_test.go](../api/regress_test.go)），用「十分钟前的签名 + 有效令牌」重注册 → `200`。评审的 `probe_register_proof` 复跑结果从 `TOKEN+BAD-SIGNATURE 401` 变成 `200`；它的对照用例 `TOKEN-ONLY` 现在回 `401`，因为 U3 把令牌改成了一次性（见下），这条对照不再是有效断言。

### [P2][F2] `ConfirmCredential` 没有总时限 —— 已修

改 [app/credential.go](../app/credential.go)：抽出 `revokeAll(ctx, ids)`，整批共用一个 `credentialRevokeTimeout`（30 秒）；`RevokeCredential` 与 `ConfirmCredential` 都走它。心跳最坏阻塞从 `N×15s` 收到 30 秒，超时返回错误、心跳记日志、留待下一轮。

验证：新增 `TestRevokeAllStopsWhenTheCallerIsDone`（[app/credential_test.go](../app/credential_test.go)），调用方取消后逐张撤销立刻停手、且不删本地行。总时限本身没有自动化用例——按 30 秒的时限写一条计时测试会让整个套件多等半分钟，收益不足以抵消；共享 helper 让两条调用路径只剩一个实现，这一点由构造与用例共同保证。

### [P3][F3] 契约数据模型停在「三张表」 —— 已修

改 `../../docs/contract/server.md`（mesh 根，不在本仓库）：改成四张表，补 `release` 表全列（含两个启动器槽位）与 `device.auto_update` 三态，补 `device_id` 非空唯一的说明。这些文件不在 `server/` 仓库里，本仓库这次提交不含它们。

### [P3][F4] `POST /api/admin/devices` 静默丢弃 `autoUpdate` —— 已修（选 a）

改 [api/devices.go](../api/devices.go)：创建请求单独用 `createDeviceRequest`，带 `optionalBool autoUpdate`；出现该字段（含 `null`）回 `400 invalid_argument` 并给出原因。与发布接口对 `appVersion` 的处理一致，也符合「拒绝静默失败」。前端 `createDevice` 本来就只发 `{name, configToml}`，不受影响。契约同步写了这条。

验证：新增 `TestCreateDeviceRejectsAutoUpdate`（三种取值都 `400`，且一台设备都没落库）；评审的 `probe_device_create_autoupdate` 从 `200` 变成 `400`。

### [P3][F5] `/admin/index.html` 被标一年不可变缓存 —— 已修

改 [api/pages.go](../api/pages.go)：`index.html` 与 `/admin`、`/admin/` 共用无缓存的入口；未知路径、`..`、取不到 `ReadSeeker` 一律 `404 not_found`，不再回落到入口文档。控制台没有客户端路由，回落只是把 404 藏成 200 `text/html`。

验证：新增 `TestConsoleEntryIsNotCachedAndMissingAssetsAreNotFound`（5 条路径的状态码与 `Cache-Control`）；`probe_static_index` 复跑：`/admin/index.html` → `no-store`，缺失资源 → `404`。

### [P3][F6] 令牌清理 ticker 在数据库与日志关闭之后才停 —— 已修

改 [main.go](../main.go)：两条退出分支都在 `application.Close()` 之前显式 `stopPurge()`，`defer stopPurge()` 留作兜底（`CancelFunc` 幂等）。

### [P3][F7] 过时注释、清单与不可达分支 —— 已修

| 位置 | 处理 |
| --- | --- |
| [app/settings.go](../app/settings.go) `Publish` 注释 | 按实现重写：先校验下载、再删/落模板（staged rename）、最后提交槽位事务；写明「模板已换新而槽位事务失败」这个剩余窗口 |
| `app/settings.go`、`api/api_test.go` 里的 `client/cmd/pack` | 改成 `build/release.ps1` |
| [.dockerignore](../.dockerignore) | 删掉不存在的 `/AGENTS.md` 与 `/*.kube` |
| [api/main_test.go](../api/main_test.go)、[app/main_test.go](../app/main_test.go) | 直接 `fakecli.Invoked(); os.Exit(m.Run())`，去掉不可达分支 |

### [P3][F8] 只设 `HOST` 时不参与监听地址 —— 已修

改 [main.go](../main.go)：`defaultListen = ":8088"` 换成 `defaultPort = 8088`，监听地址一律 `net.JoinHostPort(conf.host, port)`。契约 `server.md` 的 `PORT` 一行同步改成「未设置时用 `8088`；与 `HOST` 组合成监听地址」。

验证：`probe_host_port.ps1` 复跑，`HOST=127.0.0.1` 且不设 `PORT` → 日志 `addr=127.0.0.1:8088`，实际只监听 `127.0.0.1:8088`。

### [P3][F9] 配置原文没有单独上限 —— 已修

改 [app/device.go](../app/device.go)：新增 `MaxConfigBytes = 64KiB`，`CreateDevice` 与 `UpdateDevice` 超限即 `ErrInvalidInput`（接口回 `400 invalid_request`）。契约 `admin-api.md` 的「配置原文」一节写明了这条唯一约束与理由。

验证：新增 `TestDeviceConfigHasItsOwnLimit`：`64KiB+1` 创建与更新都 `400`，正好 `64KiB` 创建成功。

## 待确认项

| 项 | 结论 | 说明 |
| --- | --- | --- |
| U1 gzip 让摘要不等于线上字节 | 已修 | [app/settings.go](../app/settings.go) 的下载客户端加 `Transport.DisableCompression: true`，摘要覆盖客户端真正会下载到的字节 |
| U2 zstd 窗口上限 512MiB | 保留 | 只有持管理令牌的人能触发发布，收紧窗口会拒掉合法的大包；真正该做的是给解压后的字节数设上限，那需要改流式读取的写法，超出这一轮的改动范围。已在评审里记录为已知余量 |
| U3 「register 成功即清空 token」与实现不符 | 已修（改代码） | 绑定分支靠令牌证明归属时，成功后调 `ConsumeToken` 清掉；契约措辞同步成「用它换到配置即清空，首次绑定与已绑定槽位两处都清」 |
| U4 契约把 nonce 校验写在验签之前 | 已修（改契约） | `device-api.md` 改成先验签再登记 nonce，并写明理由：验不过的签名不该占用 nonce，否则伪造请求能抢先登记合法客户端要用的 nonce |
| U5 归档探针不可复跑 | 保留，定约定 | 归档探针是评审当时的快照，不随契约字段迁移维护；需要复跑时在该轮目录里重写一份（本轮就是这样做的：`probeutil` + 4 个新探针）。契约改名时同步历史探针的成本高于价值 |
| U6 `TemplateTOML` 吞掉所有读取错误 | 已修 | 改成 `(string, error)`：只有「文件不存在」与「全空白」回退内置模板，其它错误由 [api/devices.go](../api/devices.go) 回 `500 internal`。否则控制台会显示一份并非存储内容的东西，一保存就覆盖原文件 |
| U7 `credential_channel_unconfigured` 不在错误码表 | 已修 | `admin-api.md` 的错误码表补了 `409 credential_channel_unconfigured` |

> 注：U3 的改码让评审的 `probe_register_proof` 对照用例（`TOKEN-ONLY` 期望 `200`）失效——那正是这次要消除的行为：同一个令牌不再能用两次。

## 构建与测试

环境：Windows + pwsh 7，`go version go1.27.4 windows/amd64`，`GOPROXY=https://goproxy.cn,direct`。

| 命令 | 结果 |
| --- | --- |
| `gofmt -l .` | 空 |
| `go vet ./...` | exit 0 |
| `go test ./... -count=1` | exit 0：`api 14.496s`、`app 5.319s`、`builtin 1.492s`、`core 2.494s`、`db 4.447s` |
| `func Test\w+(` 计数 | 107（评审时 102，本轮 +5） |

`api` 包的耗时从 9.987s 涨到 14.496s，主要来自新增的静态资源用例与 64KiB 配置用例（大字符串的哈希与落库），以及设备创建被拒后的一次设备列表。

### 评审探针复跑

| 探针 | 现在的结果 |
| --- | --- |
| `probe_register_proof` | `TOKEN+BAD-SIGNATURE` → `200`（已修）；`TOKEN-ONLY` → `401`（令牌已消费，见 U3） |
| `probe_static_index` | `/admin`、`/admin/index.html` → `no-store`；缺失资源 → `404` |
| `probe_device_create_autoupdate` | `CREATE` → `400 invalid_argument` |
| `probe_admin_route_auth` | `routes=18 not-401=0`（未回归） |
| `probe_host_port.ps1` | `addr=127.0.0.1:8088`，只监听回环 |

## 未自动验证

- F2 的 30 秒总时限没有计时用例（理由见上），只验证了「调用方取消即停手」与「撤销失败不删行」。
- F8 只有探针复跑，没有进测试套件（它测的是 `main()` 里的地址拼装）。
- 契约文档在 mesh 根下，不由本仓库的 CI 覆盖。
