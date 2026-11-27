# mesh-server 服务端评审（`624839b..aa8da9e`）

- **提交**：`aa8da9e301d4d6a39ee4099b684c468486ece494`（完整 SHA）
- **分支**：`main`
- **评审时间**：2026-11-27T14:26:00+08:00（Asia/Shanghai），目录名 `2026-11-27T14-26-00+0800_aa8da9e`
- **评审范围**：上一轮修复提交 `624839b` .. `aa8da9e`，共 5 个提交：
  `1cf2a5b`（签发通道改走 unix socket）、`fd917a6`（部署清单补 socket 挂载与拉包白名单）、`f63030a`（部署参考入库）、
  `08626bf`（管理面拆到独立监听）、`aa8da9e`（发布签名）。附加全项目扫描（`api/`、`app/`、`db/`、`core/`、`httpx/`、`text/`、`signing/`、`tools/`、`main.go`、构建与部署文件）。
  上一轮评审目录 `code-reviews/2026-11-27T05-45-47+0800_a6b688`。前端（`frontend/` 下的 Vue/TS/CSS 与 `dist/`）按任务要求**不审**，
  只为确认字段用法 grep 过 `frontend/src/api.ts` 与 `UpdatesView.vue`。
- **审查开始时的工作区状态**：`git status --porcelain` 无输出（干净），`HEAD` = `aa8da9e`。
  本机另有被 `.gitignore` 忽略的 `.data/` 等目录，不在范围内。本轮只新增 `code-reviews/2026-11-27T14-26-00+0800_aa8da9e/`
  （`REVIEW.md` 与 `scripts/probe_signpkg/`），**没有修改生产代码、测试代码或文档，没有 commit、没有 push**。
  评审期间被跟踪文件无变动，因此没有需要排除的脏文件。
- **对照文档**：mesh 根 `docs/contract/{updates,server,client,device-api,admin-api}.md`、`docs/security.md`、`../DESIGN.md`、`./DESIGN.md`、`./README.md`。

## 结论

上一轮 F1–F9 的修复全部仍然成立（见第 1 节）。本轮新增 **6 条：2 条 P2、4 条 P3**，没有 P0/P1。
发布签名这条链本身的设计是对的：签名原文放在 `signing` 包、两端共用一个常量、验签发生在**服务端下载完并自己算完摘要之后**、
空串与非法十六进制都被拒、`Options.ReleasePublicKey` 的 nil 默认值路径指向编进二进制的公钥（`main.go` 根本不设这个字段）。
问题集中在这条链的**周边**：存量数据、离线工具的手滑风险、以及验签时机带来的无谓下载。

| 编号 | 级别 | 摘要 | 证据 |
| --- | --- | --- | --- |
| F1 | P2 | 存量发布行的签名为空串：客户端永远装不上、每 30 秒一行日志，服务端没有任何标记或告警 | 代码可证（两端） |
| F2 | P2 | `signpkg -newkey` 静默覆盖已有离线私钥；Windows 上 `0o600` 也不设 ACL | 探针复现 |
| F3 | P3 | `signpkg -sha256` 不归一大小写：大写摘要签出的签名必然验不过，报错指向「签名无效」 | 探针复现 |
| F4 | P3 | 发布时先整包下载再验签：签名缺失/格式错也白白下载最多 200 MB | 代码可证 |
| F5 | P3 | `internal/testenv` 的 `config.releasePrivate` 是没有任何 setter 的死钩子 | 代码可证 |
| F6 | P3 | `app/rpcsocket.go` 的桥：监听器不关、每连接 2 个 goroutine 无上限、`io.Copy` 无超时 | 代码可证 |

## 1. 上一轮（`a6b688`）修复核验

| 上轮编号 | 结论 | 依据 |
| --- | --- | --- |
| F1 绑定槽位带无效签名时不再拒有效令牌 | 仍然成立 | `api/device_api.go:199-211` 签名分支失败后继续 `slotTokenValid`，`:152-155` 消费令牌；本轮只在同一文件的 `updateInfo`/`launcherInfo` 里加字段，未动这段 |
| F2 `ConfirmCredential` 无总时限 | 仍然成立 | `app/credential.go:132-134` `credentialRevokeTimeout = 30s`，`:206-208` 在 `revokeAll` 里 `WithTimeout`；`:92` 单次 cli 15 秒上限也在 |
| F6 令牌清理 ticker 在关库之后才停 | 仍然成立 | `main.go:190-194` 建 `purgeCtx`，`:204/209/231` 三条退出路径都先 `stopPurge()` 再 `application.Close()`/`logFile.Close()` |
| F8 只设 `HOST` 时监听地址忽略 `HOST` | 仍然成立 | `main.go:116` `net.JoinHostPort(conf.host, ...)`，`:32-34` 注释写明；`:272-274` `HOST` 缺省 `0.0.0.0` |
| F3/F4/F5/F7/F9（契约数据模型、`POST /devices` 丢 `autoUpdate`、静态缓存头、过时注释、配置原文无上限） | 本轮未复验 | 这五条落在 `frontend`/`docs`/静态资源与巡检性代码上，与本轮改动的文件不重叠；`api/api.go` 有 136 行改动，但 diff 只涉及管理面路由注册（`HandlerFor`/`SurfaceAdmin`/`SurfaceDevice`），不动 `devices.go` 的字段处理 |
| U1 一次性迁移 | 未动，仍在 | `db/db.go:167/187/204` 的 `renameAppHashColumn`/`dropReleaseHashColumn`/`dropLegacySchema` 原样保留 |

本轮改动的 `api/api.go` 把路由按 `SurfaceAdmin`/`SurfaceDevice` 拆成两份，`main.go:140` 只把设备面交给主端口、`:161-176` 才在 `ADMIN_PORT` 上挂管理面。
`api/surface_test.go` 有 72 行用例盯住「主端口上没有管理路由」，`08626bf` 的设计是有意为之且已被 `DESIGN.md:186` 写下来。

## 2. 本轮发现

### [P2][F1] 存量发布行的签名为空串：客户端永远装不上、每 30 秒一行日志，服务端没有任何标记或告警

- **位置**：迁移只补列、不补值——[db/db.go:146-153](../../db/db.go#L146-L153) 用 `ensureColumn("release", "signature", "TEXT NOT NULL DEFAULT ''")`，
  注释写「旧记录留空串——它们本来就发不出去，重新发布会补上」；下发侧没有任何过滤：
  [api/device_api.go:264-274](../../api/device_api.go#L264-L274) 的 `launcherOf` 只看 `LauncherSHA256 != ""` 就把整个槽位（含空 `signature`）发下去，
  [api/device_api.go:354-361](../../api/device_api.go#L354-L361) 的客户端包同样只按摘要判断「已知」，不看签名是否为空。
  客户端对应位置：`crates/engine/src/update.rs:173-189` 与 `:500-513`（验签不过就写日志并返回）。
- **触发条件**：升级控制面时 `release` 表里已有行（升级前发布过客户端包、启动器，或两者都有），部署后不重新发布。
- **实际结果**：
  1. 客户端槽位：设备版本更旧 → `appChanged` 为真 → 每轮心跳下发同一份（空的）签名 → 客户端拒装并写一行 `update_signature_invalid`。
     30 秒一行、一天 2880 行、永不更新，直到管理员重新发布。**这是「升级控制面之后全家再也不更新」的形态**，而管理面板显示一切正常。
  2. 启动器槽位：`launcherOf` 不看签名，所以每轮心跳都带一个必被拒的启动器；客户端写 `launcher_signature_invalid`，同样 30 秒一行。
  3. 服务端没有任何启动告警或接口字段能提示「这一行缺签名」；`releaseView` 会把 `appSignature: ""` 交给前端，前端是否给提示属前端范围。
- **为什么不是 P1**：只要发布流程本身更新了（`build/publish.ps1` 已带 `-key`/`signpkg`，见探针一节的旁证），第一次重新发布就会把签名补上；
  真正会咬人的是「先升级控制面、客户端逐步升级，中间存量行没人管」这个窗口。
- **修复建议**：
  - 服务端启动时扫一遍 `release` 表，把「有摘要无签名」的平台打成一条 `WARN`（或提供 `/readyz` 之外的一个诊断），并在 `releases` 响应里显式给出 `usable: false`；
  - `launcherOf` 对空签名返回 `nil`（等价于「这个槽位没发布」），避免每 30 秒给全网发一个必被拒的槽位；
  - 客户端把「同一 `(version, sha256)` 的验签失败」按版本去重记日志（与已有的失败计数复用），或至少不要每轮心跳都写；
  - `db/db.go` 那句注释应当改成事实：「旧记录在新客户端上一律被拒，必须重新发布」。

### [P2][F2] `signpkg -newkey` 静默覆盖已有离线私钥；Windows 上 `0o600` 也不设 ACL

- **位置**：[tools/signpkg/main.go:45-47](../../tools/signpkg/main.go#L45-L47)（`-newkey` 分支不检查目标是否存在），
  [tools/signpkg/main.go:64-78](../../tools/signpkg/main.go#L64-L78)（`createKey` 直接 `os.WriteFile(path, block, 0o600)`，O_CREATE|O_TRUNC）。
- **触发条件**：把 `-newkey` 再跑一次在同一个 `-key` 路径上（默认路径是 `build/release.key`，见 `build/publish.ps1:27`）。
- **实际结果**（探针 `scripts/probe_signpkg` 实测，同一条命令连跑两次）：
  `pub1 = 327aa3cc…34b9`、`pub2 = 31df1215…22cf`、`overwritten = true`、`old key still on disk = false`。
  即：**旧的发布私钥被无声销毁，新公钥与已发布客户端里编着的常量（`signing/signing.go:17`、`client crates/remote/src/signing.rs:13`）不再匹配**。
  后果是不可恢复的——已经发出去的客户端只认旧公钥，用新私钥签的任何包都会被全网拒装，只能再发一版换公钥的客户端（而那版客户端必须先能用旧私钥签出来）。
  另外 `0o600` 在 Windows 上只影响只读属性，不设置 ACL：私钥文件的权限仅来自所在目录的继承 ACE（`build/` 是普通目录）。
- **修复建议**：`createKey` 在目标存在时直接失败（`os.Stat` → 已存在则 `fail`），要覆盖必须显式加 `-force`；
  写入前用 `os.OpenFile(..., O_CREATE|O_EXCL|O_WRONLY, 0o600)`；Windows 上额外收紧一次 ACL（或至少在成功时打印文件权限供核对）；
  成功时打印的应包含「公钥要同时写进两端源码」的提示，避免只改了一端。

### [P3][F3] `signpkg -sha256` 不归一大小写：大写摘要签出的签名必然验不过

- **位置**：[tools/signpkg/main.go:55-60](../../tools/signpkg/main.go#L55-L60)：`hex.DecodeString(*sum)` 接受大写，
  但 [signing/signing.go:43-45](../../signing/signing.go#L43-L45) 把 `sha256` 原样拼进签名原文；
  而控制面自己的摘要来自 [app/settings.go:400-410](../../app/settings.go#L400-L410) 的 `hex.EncodeToString`（永远小写），客户端也一样。
- **触发条件**：运维从别处粘一个大写的 sha256（`Get-FileHash` 默认大写）传给 `-sha256`。
- **实际结果**（探针实测）：`lowercase digest verifies = true`、`uppercase digest verifies = false`；
  且 `signpkg` 此时**退出码 0**、正常打印一个 128 位十六进制签名。发布时报 `appSignature 无效`（`app/signature.go:21-23`），
  错误信息把人往「签名算错了/私钥不对」引，而不是「摘要大小写」。`build/publish.ps1:37` 自己做了 `.ToLower()` 所以脚本路径不会踩到，手敲命令会。
- **修复建议**：`-sha256` 解析后统一 `hex.EncodeToString(raw)` 再拼签名原文（或直接拒绝含大写字母的输入），并顺手校验长度就是 32 字节。

### [P3][F4] 发布时先整包下载再验签：签名缺失/格式错也白白下载最多 200 MB

- **位置**：[app/settings.go:207-238](../../app/settings.go#L207-L238)：`fetchClientPackage` 在 `:220`（下载 + 算摘要 + 解包读 `version.json`），
  验签在 `:232`；[app/settings.go:242-263](../../app/settings.go#L242-L263) 的启动器槽位同理（`:254` 下载，`:258` 验签）。
  `api/settings.go:134-141` 只校验「给了签名就得给 URL」，不校验签名本身的形状。
- **触发条件**：`appSignature` 为空串（漏填）或长度不是 128 位十六进制（粘错、截断）。
- **实际结果**：每次失败都先把整包拉一遍（上限 `MaxReleaseBytes` = 200 MB，超时 60 秒），然后在本地报「签名缺失/无效」。
  管理员重试一次就再拉一次；对方文件服务在公网时这是实在的带宽与时间。
- **修复建议**：在 `fetchClientPackage`/`fetchDigest` 之前先做形状校验——`signature == ""` 报「缺少签名」，
  `hex.DecodeString` 失败或长度不是 64 字节报「签名格式不对」。`signing` 包加一个 `validSignatureShape(string) error` 即可（约 8 行）。

### [P3][F5] `internal/testenv` 的 `config.releasePrivate` 是没有任何 setter 的死钩子

- **位置**：[internal/testenv/testenv.go:76-78](../../internal/testenv/testenv.go#L76-L78) 声明 `config.releasePrivate`，
  [:120-136](../../internal/testenv/testenv.go#L120-L136) 读它决定「用给定的私钥还是现生成一对」。
- **实际结果**：`grep -r releasePrivate --include=*.go` 只有这三处（声明、注释、读取），**没有任何 `Option` 会把值写进去**；
  所有环境都走「现生成一对」。这是典型的「只为将来可能的测试留的口子」，属于 `AGENTS.md` 2.6.2「测试不应污染生产设计」的反面。
- **修复建议**：删掉这个字段与 `:120` 的分支（生成密钥对是无条件且正确的行为），或补上真正使用它的 `Option` 并给一个用例。

### [P3][F6] `app/rpcsocket.go` 的桥：监听器不关、每连接 2 个 goroutine 无上限、`io.Copy` 无超时

- **位置**：[app/rpcsocket.go:21-35](../../app/rpcsocket.go#L21-L35)（`net.Listen("tcp", "127.0.0.1:0")` 之后 `go Accept` 循环，`listener` 永不 `Close`，没有引用交还给调用方），
  [:39-51](../../app/rpcsocket.go#L39-L51)（每连接一个新 goroutine + 一个 `bridgeRPCConn`，里面再起两个 `io.Copy`，无 deadline、无连接数上限）。
  调用点 [main.go:87-90](../../main.go#L87-L90)。
- **触发条件**：容器内任意进程不断连上那个回环临时端口且不主动关闭；或 `systemd-socket-proxyd` 一侧卡住。
- **实际结果**：每个卡住的连接常驻 3 个 goroutine 与 2 个 fd，无上限、无超时、无回收；进程退出前监听器一直在。
  另外这条桥的**安全含义与 [DESIGN.md:159-165](../../DESIGN.md#L159-L165) 的表述不一致**：那里写「签发权因此从『网络可达』变成『文件系统可达』」，
  但桥在容器回环上开了一个无鉴权的 TCP 口，任何能连 `127.0.0.1` 的容器内进程都能拿到签发权——实际边界是「容器内回环」，不是「文件系统」。
  单二进制容器里其它进程不多，所以只是表述与余量问题，不是当前漏洞。
- **修复建议**：把 `listener` 与一个 `stop` 函数交还给调用方并在 `main` 退出时关闭；`bridgeRPCConn` 给两端设 `SetDeadline`（例如 30 秒）；
  用带缓冲的信号量限制并发连接数（`easytier-cli` 一次只跑一条，上限 4 就够）；把 `DESIGN.md` 那句话改成「容器内回环 + 文件系统双闸」。

## 3. 引入库的收益分析

本节只评估本区间新增/改动的代码。行数为实测（`(Get-Content x).Count` 或 diff 统计）。

### 3.1 值得引入（强收益）

**本轮没有一条够格。** 逐项排除的理由：

| 候选 | 现状（文件:行数） | 为什么不够强 |
| --- | --- | --- |
| `minisign` / `sigstore` / `cosign` 取代自研签名原文 | `signing/signing.go` 58 行 + `app/signature.go` 25 行 + `tools/signpkg/main.go` 104 行 = 187 行 | 这些工具签的是**文件**；这里必须签「服务端自己下载后才算出的摘要 + 槽位 + 平台 + 版本」，私钥在离线机器上、被签的字节在控制面上。要凑成「先把文件搬到离线机再签」会毁掉整个设计的核心性质（详见 3.3） |
| `golang-migrate` / `goose` / `atlas` 取代手写迁移 | `db/db.go` 的 `createSchema`+`ensureColumn`+`hasColumn`+三个一次性迁移共约 161 行（`:107-267`） | 只有一个 SQLite 文件、6 条一次性动作、无并发部署；引入框架要新建版本表、把 `ensureColumn` 的幂等语义改写成有序 SQL 文件，行数不减反增，换来的「标准工具链」这个项目并不缺 |
| `golang.org/x/text/message` 取代 `text` 包 | `text/text.go` 151 行 + `text/ids.go` 49 行 + `report_*.go` 20 行 | 清单仍是自定义 `.lang`（51/52 行），解析与校验（`text.go:70-140`）仍要自己写；真正能省的只有渲染那约 40 行，却要多引一棵 `x/text` |

如果一定要在「工具」层面找一个强收益，本轮的正确做法已经发生了：`build/publish.ps1` 把「算摘要 → 调 signpkg → PUT」串成一步，
而不是让人手敲 `signpkg` 再手填 128 位十六进制——那才是这类签名链最容易出错的地方，而它不需要任何新依赖。

### 3.2 可以引入但收益一般

1. **`github.com/go-playground/validator`**（发布请求体校验）：`api/settings.go:121-141` 现在是 21 行手写 `if`（槽位三字段同生同灭、签名必须与 URL 一起给）。换成 tag 校验能把这段压到 8 行左右，但也会把「槽位三字段必须同时给」这种业务规则藏进结构体 tag——这里手写反而更可读，收益一般。
2. **`github.com/stretchr/testify`**：`api/api_test.go` 有 1700+ 行手写 `t.Fatalf` 断言。省的是样板不是逻辑，项目风格已统一，纯增益但不值得改存量。
3. **标准库 `fmt.Sprintf` 取代 `text.Format` 的 `{n}` 渲染**（`text/text.go:40-69`，约 30 行）：能省 30 行，但 `fmt` 的动态格式串过不了 `go vet` 的 printf 检查，参数错配会渲染成 `%!s(int=1)` 这种垃圾；`{n}` 现在错配只留一个 `{1}`，更可控。收益一般，**不建议换**。
4. **`golang.org/x/time/rate`**：并发/限流现在是 `chan struct{}` 信号量（`api/` 里若干处），对这个规模够用。
5. **`github.com/klauspost/compress/zstd` 换成标准库**：不存在，标准库没有 zstd（`compress/zstd` 仍未进入稳定 API）。已在用，保持。

### 3.3 不该引入

1. **`minisign`/`sigstore`/`cosign`**：见 3.1。签名对象的形状（元数据而非文件）决定了它们不适用；硬套会把「离线私钥只签摘要」变成「离线机要拿到整包」。
2. **`crypto/ed25519` + `encoding/hex` 换成第三方签名库**：本区间的签名与验签**没有引入任何新依赖**（`go.mod` 未变，全 stdlib）。这是正确的，别动。
3. **`dpapi`/`golang.org/x/sys/windows` 之类的 Windows 加密封装**：服务端不碰 Windows API。
4. **`godotenv`/`viper` 取代 `main.go` 的环境变量解析**（`main.go:242-330` 约 90 行）：这里每个变量都有白名单/范围校验与「非法即拒绝启动」的语义（如 `:302-304` 的 `ADMIN_PORT`），`viper` 会把校验又搬回业务代码，且引入一棵大依赖。
5. **`gorilla/websocket`/`gin` 之类框架取代 `net/http` + `httpx`**：现有路由与中间件（并发信号量、请求体上限、`client_ip` 采信）都是手写且与安全模型绑定的，换框架等于重写安全模型。

## 4. 待确认

1. **上线顺序**。F1 的实际伤害取决于「先升控制面还是先升客户端」。如果控制面先升、而存量行没有立刻重新发布，中间这段时间全网都收不到更新（每 30 秒一行日志）。
   建议在 `docs/contract/updates.md` 的「发布签名」节里写一句「升级控制面后必须重新发布所有槽位」，这一句目前没有。
2. **`build/release.key` 就在开发机上**。`build/publish.ps1:27` 默认读它，本机确实存在；而 `signing/signing.go:13-16` 与 `crates/remote/src/signing.rs:10-12` 的注释都写「私钥离线，不在这台机器上」。
   代码没有问题，但这条注释陈述的前提在本机不成立——需要确认生产发布是否真的在离线机器上执行，否则「控制面被拿下也换不了包」的结论要打折扣。
3. **`build/publish.ps1:14` 的 `$AdminToken` 默认值是字面量**。该文件不入库（`AGENTS.md` 约定 `build/` 不入库），但默认参数里带一个长期管理员令牌，
   只要这个文件被拷走/贴进 issue，管理面就等于公开。不在本次两个仓库的范围内，仅提示。
4. **管理面拆监听的升级影响**。`main.go:137-141` 在未设 `ADMIN_PORT` 时**一条管理路由都不注册**（主端口上 `/api/auth`、`/api/admin`、面板全部 404）。
   `k8s.mesh-server.yaml:19-20` 已设 `ADMIN_HOST`/`ADMIN_PORT`，[DESIGN.md:192-193](../../DESIGN.md#L192-L193) 也写明了；但任何用别的部署方式（docker run、旧 compose、本机 `just run`）升级的人会突然失去面板且没有回退。是否需要在启动日志里把这条 WARN 提到更显眼（现在 `main.go:187` 是一行 `logger.Warn`），由部署方判断。
5. **`releaseView` 把签名原样回给管理端**（`api/settings.go:23/29/64/70`）。这是一次「把签名当可编辑字段」的暴露：前端若在编辑备注时把签名丢掉，下一次发布会 400。
   前端不在本次范围，未验证 `UpdatesView.vue` 是否原样回传。
6. **`signing` 包在 `init` 里 `panic`**（`signing/signing.go:29-35`）。常量写错时进程起不来——对服务端是合理的 fail-fast，但 `tools/signpkg` 也会跟着 panic；
   如果将来有人把公钥改成从环境变量读，这段逻辑要一起改。

## 5. 构建与测试（实际执行）

工作目录 `S:\local\repos\mesh\server`，`go1.27.4`，`GOPROXY=https://goproxy.cn,direct`。

| 命令 | 结果 |
| --- | --- |
| `gofmt -l .` | 无输出（含 `code-reviews/` 下的探针；加了探针之后重跑仍无输出） |
| `go vet ./...` | 通过（无输出） |
| `go test ./... -count=1` | 通过：`mesh-server` 2.4s、`api` 9.3s、`app` 2.7s、`builtin` 1.4s、`core` 1.7s、`db` 2.7s、`signing` 1.4s、`text` 1.5s；`httpx`/`internal/*`/`tools/signpkg` 无测试文件 |

探针（`scripts/probe_signpkg`，`go run ./code-reviews/<dir>/scripts/probe_signpkg`）实测输出：

```
newkey twice on the same path:
  pub1 = 327aa3cccac3f1e3c56a7bfd65be9304eefca202e74c7877e0c546bcdb0c34b9
  pub2 = 31df1215ab463fb14e7b6daa54df8896d95952c51410154d32a3c349acf722cf
  overwritten = true
  old key still on disk = false
signing 2026.11.27-103:
  lowercase digest verifies = true
  uppercase digest verifies = false
```

## 6. 未验证项

- 前端（`frontend/**`）一律未构建、未 typecheck、未阅读；`UpdatesView.vue` 只 grep 过字段名。
- 真实 `easytier-cli` 经 unix socket 桥接的端到端行为：本机没有种子、没有 `/run/mesh/rpc.sock`，`app/rpcsocket_test.go` 只覆盖到「socket 不存在/不是 socket 时拒绝启动」与基本转发。
- `mesh-rpc.service`/`mesh-rpc.socket` 的实际部署（`systemd-socket-proxyd` 的权限、`SocketMode=0600` 与容器映射用户的匹配）未在目标机验证。
- 管理面独立监听的部署验证：只跑了 `api/surface_test.go`，没有真的用 `ADMIN_PORT` 起两个监听再压一遍反向代理路径。
- 上线迁移：没有用真实存量 `release` 表（含空签名行）跑过一次升级；F1 的后果按代码与客户端实现推断。
- Linux 生产镜像构建（`Dockerfile`）未跑。
