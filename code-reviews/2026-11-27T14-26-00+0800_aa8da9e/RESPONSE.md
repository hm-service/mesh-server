# 服务端评审回应

- **对应评审**：`code-reviews/2026-11-27T14-26-00+0800_aa8da9e/REVIEW.md`
- **评审提交**：`aa8da9e`
- **回应时间**：2026-11-27T15:05:00+08:00（Asia/Shanghai）
- **范围**：本轮全部 6 条（F1/F2 P2、F3–F6 P3）。上一轮 F1/F2/F6/F8 的复核结论无异议；F3/F4/F5/F7/F9 落在前端与文档，本轮未动。

## F1（P2）存量发布行签名为空 → 已修复

**做法**：把「有摘要无签名」的槽位当成**未发布**，而不是「发了必被拒」。

| 位置 | 改动 |
| --- | --- |
| `api/device_api.go` `launcherOf` | `LauncherSignature == ""` 时返回 `nil`，等价于这个槽位没配 |
| `api/device_api.go` `appChanged` | 加 `release.Signature != ""` 前置条件，空签名不收也不判版本 |
| `main.go` | 启动时扫一遍 `release` 表，把「有版本无签名」的平台逐条打 `WARN` |
| `db/db.go` | 注释从「旧记录本来就发不出去」改成事实：空签名等于未发布，必须重新发布一次 |

客户端侧没有加「按版本去重记日志」的状态，理由写在 `client/.../RESPONSE.md` 的 F3：那是症状，根因在这里。**请下一轮评审接受或驳回这个取舍。**

**验证**：新增 `TestUnsignedReleaseIsNotOffered`——绕过管理接口直接往库里写一行「有摘要无签名」的记录（接口本身已经强制签名），心跳必须回 `204`。

## F2（P2）`signpkg -newkey` 静默覆盖私钥 → 已修复

**做法**（`tools/signpkg/main.go`）：`createKey` 改用 `os.OpenFile` 加 `O_CREATE|O_WRONLY|O_EXCL`，已存在时返回 `fs.ErrExist` 并提示「加 `-force` 才会覆盖」；新增 `-force` 开关，只有显式给了才 `O_TRUNC`。写入后 `Close` 的错误也检查。公钥改成同时往 stderr 打一行提示：这个值要写进两端源码。

**没有做**：Windows 上的 ACL 收紧。`signpkg` 是离线工具、跑在哪台机器由发布流程决定；用 Go 标准库设文件 ACL 要引 `golang.org/x/sys/windows` 再写一段 `SetNamedSecurityInfo`，收益与复杂度不成比例。改成「存在即失败」之后，最危险的路径（覆盖掉唯一一把私钥）已经堵住。

**验证**：探针 `scripts/probe_signpkg` 的复现路径现在会失败、退出码非零。没有加自动化测试：`signpkg` 是 `package main`，为这一个开关拆成可测包不值得。

## F3（P3）`-sha256` 不归一大小写 → 已修复

**做法**：解析后统一 `hex.EncodeToString(raw)` 再拼签名原文。粘一个大写摘要进来，签出来的就是小写摘要的签名——与控制面自己算的一致，不会再出现「签了但永远验不过」还要人去猜原因。

## F4（P3）先整包下载再验签 → 已修复

**做法**：`app/signature.go` 拆出 `checkReleaseSignatureShape`（空串 → `release_signature_missing`，非 128 位十六进制 → `release_signature_invalid`），`prepareClientSlot` / `prepareLauncherSlot` 在**下载之前**调用它；`VerifyReleaseSignature` 内部也复用它，两处文案因此只有一份。

`signing.ValidShape` 是新加的纯形状检查，只说「像不像签名」，不说「对不对」。

**验证**：新增 `TestMalformedSignatureFailsBeforeDownloading`——URL 指向不可达地址、签名是 `"00"`，必须报签名错误且**不能**出现「无法读取 appUrl」。`signing` 包新增 `TestValidShape`。

## F5（P3）`testenv` 的死钩子 → 已修复

删掉 `config.releasePrivate` 字段与它的分支，`New` 里无条件生成一对密钥。

## F6（P3）RPC 桥的资源问题 → 已修复

**做法**（`app/rpcsocket.go`）：

| 问题 | 改动 |
| --- | --- |
| 监听器永不关闭 | 签名改成 `(addr, stop func(), err)`，`main.go` 用 `defer stopBridge()` |
| 每连接 goroutine 无上限 | 4 槽信号量，满了直接关掉新连接 |
| `io.Copy` 无超时 | 两端 `SetDeadline` + `DialTimeout`，都是 30 秒 |

`DESIGN.md` 第 7 节的措辞按评审意见改了：签发权是「宿主 socket 文件权限」加「能进这个容器的网络命名空间」两道闸，不是单说文件系统。

**验证**：`app/rpcsocket_test.go` 跟着改了调用形式，转发用例通过；连接数上限与超时没有专门的测试（要在 Linux 上造卡住的连接，而且测的是资源回收而不是协议）。

## 构建与测试

工作目录 `server/`，`go1.27.4`，`GOPROXY=https://goproxy.cn,direct`。

| 命令 | 结果 |
| --- | --- |
| `gofmt -l .` | 无输出 |
| `go vet ./...` | 通过 |
| `go test ./... -count=1` | 通过：8 个包全 ok（`signing` 新增 1 条、`api` 新增 2 条） |

## 未自动验证项

- F1 的真实存量库升级没跑：用 `db.PublishSlots` 直接造出「空签名行」来测，等价于迁移之后的状态，但没有在真机上从头跑一遍 `ensureColumn`。
- F2 的 Windows ACL 收紧没做，理由见上。
- F6 的连接上限与超时行为没有自动化测试；收发转发本身有 Linux 用例。
- 前端的签名输入框按任务要求未审、未构建。