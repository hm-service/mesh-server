# mesh-server 修复回应（评审 2026-11-26T07-08-57+0800_b56687）

- **对应评审**：[REVIEW.md](./REVIEW.md)（评审 commit `b5668798538d4aa1b1da2bab88c5a5e1a81a6db6`，分支 `main`）
- **回应时间**：2026-11-26 07:30 +08:00（Asia/Shanghai）
- **仓库**：`S:/local/repos/mesh/server`（独立 git 仓库）
- **工作区状态**：改动全部留在工作区，**未 commit、未 push、未部署、未重启任何在线进程**。`../client` 与线上主机全程未动。
- **范围**：F1–F6 全部处理；REVIEW 的「待确认」U1、U2 按任务要求不动（理由见文末）。

## 结论摘要

| 编号 | 级别 | 结论 |
| --- | --- | --- |
| F1 版本标识 | P2 | 已修（按用户当天调整的方案：发布不再收版本号，版本从包内读出；心跳按版本序比较） |
| F2 绝对地址回退 | P3 | 已修：删 `api/url.go`，直接用 `release.URL`/`LauncherURL` |
| F3 过时注释与清单 | P3 | 已修：6 处逐条改掉（`/readyz` 说法、两处测试注释与夹具布局、`purgeLoop` 注释、`.gitignore`、CI `paths-ignore`） |
| F4 重复实现 | P3 | 已修：合并凭据结果类型、删 3 个多余成员、令牌清理只留 ticker、`Authorize` 私有化 |
| F5 空白 `default_toml` | P3 | 已修：定义为「删除自定义模板、回退内置模板」，写进 CONTRACT 1.5 |
| F6 校验覆盖 | P3 | 已修：管理面鉴权补 3 条路由，CI 加 `gofmt -l` 门禁 |
| U1 / U2 | 待确认 | 未动，理由见文末 |

## F1（P2）版本标识：按版本序比较，版本只来自包内 `version.json`

**方案调整**：本轮先按「表单版本与包内版本比对」实现过一版，用户随后定下更彻底的方案，已照此改完：
发布客户端包**不再接受 `app_version`**，服务端在读整包算 sha256 的同时解出 `version.json`，用包里的 `version` 作为发布版本。因此不存在「两个版本对不上」的情况，比对那条逻辑随之删除。

**位置与做法**：

- [app/settings.go:196](../../app/settings.go#L196) `prepareClientSlot`：只收 `{app_url, app_notes?, auto_update?}`（新类型 [app.ClientPackage](../../app/settings.go#L114)），版本由 [fetchClientPackage](../../app/settings.go#L372) 读出。
- [app/settings.go:411](../../app/settings.go#L411) `packageVersion`：zstd 解压 + tar 流式遍历，找包根 `version.json` 的 `version`；下载同时 `TeeReader` 进 sha256，读完清单后把余下字节抽干，摘要仍覆盖整包（≤200MB，清单 ≤64KB）。包根就是 `bin/` 的内容（`client/cmd/pack`），清单与二进制平铺。
- 拒绝路径（均 400 `invalid_argument`、不写任何槽位）：包内没有 `version.json`、清单解析失败、`version` 缺失、`version` 不符合 `YYYY.MM.DD-N` 或超过 64 字符。[api/settings.go:105](../../api/settings.go#L105) 对请求里出现的 `app_version` 也直接 400，而不是静默忽略。
- 版本序比较：[app/version.go](../../app/version.go) 新增 `CompareVersions`（按 YYYYMMDD 与当日序号做数值比较）、`NeedsUpdate`、`ValidAppVersion`。
- 心跳判定：[api/device_api.go:291](../../api/device_api.go#L291) 改为 `appChanged := app.NeedsUpdate(req.AppVersion, release.Version)`；设备版本更高（回滚过发布、手工装过新包）或无法解析时不下发，从未上报版本时按更旧处理（装完新包就开始上报，不会循环）。
- 控制台状态：[app/device.go:336](../../app/device.go#L336) 的 `AppState` 复用同一函数，设备更新时显示 `current`（未新增 `ahead` 状态，契约按用户口径「更高即 current」）。
- 前端（本次允许且必须改）：[UpdatesView.vue](../../frontend/src/components/UpdatesView.vue) 客户端包去掉版本号输入框，改为说明「版本从包内 version.json 读出」，发布后按服务端返回值回显版本；[api.ts](../../frontend/src/api.ts) 的 `ReleasePublish` 删掉 `app_version`。启动器槽位不动。
- 文档：`../CONTRACT.md` 1.4（`app_state` 语义、发布请求字段）、1.5（心跳判定、发布流程、`app_version` 不再作为输入）、3 节验收项逐条同步；[DESIGN.md](../../DESIGN.md) 的页面表与「发布包」安全模型行同步。

**验证**：

- 单测 [app/version_test.go](../../app/version_test.go)：`2026.11.26-9 < 2026.11.26-11`（字符串序相反）、`-99 < -100`、日期优先（`2026.11.25-99 < 2026.11.26-1`）、跨年/跨月、等值，以及 7 种非法标签；`NeedsUpdate` 覆盖更旧/相同/更新/未发布/未上报/不可解析。
- 回归 [api/api_test.go:1342](../../api/api_test.go#L1342) `TestHeartbeatOnlyUpgrades`：发布 `2026.11.26-11`，设备报 `-9` → 200 下发且 `app_state=stale`；报 `-12` → 204 无响应体且 `app_state=current`；报 `2026.11.25-99` → 下发（日期优先）。
- 回归 [api/api_test.go:1283](../../api/api_test.go#L1283) `TestClientPackageVersionComesFromThePackage`：包内 `version` 直接成为 `app_version`；缺清单 / `version=build-2026-11-26` / 根本不是 tar.zst 三种都 400，且已发布的行不被改动。
- 回归 `TestReleaseRejectsIncompletePublish`：带 `app_version` 的请求 400 并提示 version.json；`app_notes`、`auto_update` 单独提交 400。
- 数据面的边界：库里已有的、不是 `YYYY.MM.DD-N` 的旧发布版本无法排序，`NeedsUpdate` 按「不更旧」处理（不下发、控制台算 `current`）；重新发布一次客户端包就会用包内版本覆盖该行。这是「不做向后兼容」的取舍，没有为旧标签保留字符串比较分支。
- 真实包端到端（一次性，跑完删除）：用 `client/cmd/pack` 打出 `version=2026.11.26-11`、`sha256=2200dcbe…` 的 tar.zst，喂给真实下载/解析路径 `fetchClientPackage`，得到的版本与摘要与 pack 输出逐字一致。

## F2（P3）删掉绝对地址回退

- 删除 `api/url.go`（整个文件）：scheme/Host/`X-Forwarded-Proto` 拼接分支随之消失。
- [api/device_api.go:238](../../api/device_api.go#L238) `launcherOf` 去掉不再需要的 `*http.Request` 参数，直接用 `release.LauncherURL`；心跳里 `app_update.url` 直接用 `release.URL`。发布入口已保证两者是绝对 http(s) 地址（`validateSlotURL`）。
- [DESIGN.md:19](../../DESIGN.md#L19) 改成现状：监听地址只来自 `HOST`/`PORT`；`X-Forwarded-For`/`X-Real-IP` 只在请求来自 `TRUSTED_PROXIES` 内的反代时采信。

**验证**：`grep -r absoluteURL`、`X-Forwarded-Proto`（排除 `code-reviews/`）零命中；`go build`/`go vet`/`go test` 全绿；`TestHeartbeatSemantics`、`TestHeartbeatCarriesLauncher` 断言下发的 URL 与存储值相同（现有测试即覆盖）。

## F3（P3）过时注释与清单

| 位置 | 改法 |
| --- | --- |
| [justfile:22](../../justfile#L22) | 改成「占位版 `/admin` 是 404，`/readyz` 仍是 200（就绪只看 `DATA_DIR` 与数据库）」 |
| [api/api_test.go:1019](../../api/api_test.go#L1019) | 去掉「version directory」，改成「tar.zst 解到 `bin/`」 |
| [api/api_test.go:209](../../api/api_test.go#L209) | 「signature headers」改成：签名在请求体里，`headers` 只传业务头 |
| 测试夹具 `releasePkg` | 顺带修正布局：包根平铺 `mesh-client.exe`、`easytier-core.exe`、`version.json`（原来写 `bin/easytier-core.exe`，与 `client/cmd/pack` 的真实包不符） |
| [app/app.go:128](../../app/app.go#L128) | 删掉不存在的 `purgeLoop` 说法，改成「main.go 里的 ticker 每分钟调用一次」 |
| [.gitignore](../../.gitignore) | 删掉「旧的编译产物」四条（`/mesh-server`、`/mesh-server.exe`、`/mesh-server-linux`、`dist/`），它们都没有生产者（当前产物是 `.dist/mesh-server-<os>-<arch>`，由 `.*` 规则覆盖） |
| CI `paths-ignore` | 删掉仓库里不存在的 `AGENTS.md`、`mesh-server.kube`、`LICENSE`（保留真实存在的 `DESIGN.md`/`justfile`/`k8s.mesh-server.yaml`/`README.md`/`docs/**`/`.gitignore`） |

**注意（需人工处理）**：删掉 `dist/` 的忽略条目后，本机残留的 `server/dist/mesh-server-linux`（18MB，旧构建脚本产物，未跟踪）会出现在 `git status`。我没有删除它，只在 `.git/info/exclude`（本地文件、不入库）加了一条 `/dist/`，避免 `just push` 的 `git add -A` 把它带进提交。是否删掉该目录由本机主人决定。

## F4（P3）重复实现与多余抽象

- 凭据结果类型合并：删掉私有的 `issuedCredential`，`credentialIssuer.generate` 直接返回 `IssuedCredential`；[app/credential.go:156](../../app/credential.go#L156) `IssueCredential` 不再逐字段拷一次。
- 删 [app/device.go](../../app/device.go) 的 `ReleaseVersion`，两个调用点直接用 `release.Version`（[api/devices.go:61](../../api/devices.go#L61)、[:132](../../api/devices.go#L132)）；对应单测断言一并删除。
- 删 [db/device.go](../../db/device.go) 的 `Count()`（生产代码无调用者），[db/group_test.go:117](../../db/group_test.go#L117) 改用 `List()` 断言数量。
- 过期令牌清理只留 [main.go:348](../../main.go#L348) 的 ticker；删掉 `app.Open` 里启动时那次清理，`PurgeExpiredTokens` 注释写明由 ticker 调用。
- [internal/testenv/testenv.go:201](../../internal/testenv/testenv.go#L201) `Authorize` 改成包内私有 `authorize`（唯一调用者是同文件 `Do`）。

**验证**：`grep` 上述符号（`ReleaseVersion`、`issuedCredential`、`Del count`、`Authorize(`、`purgeLoop`）零命中；`go vet`、`go test ./...` 全绿。

## F5（P3）空白 `default_toml`：删除自定义模板、回退内置

选了 REVIEW 的 (b) 方案：[app/settings.go:162](../../app/settings.go#L162) 在 `Publish` 里遇到空/全空白 `default_toml` 时删除 `DATA_DIR/peer.default.toml`（不存在不算错误），不再「返回 200 但什么也没发生」。「没有自定义模板」因此只有「文件不存在」一种表示，`TemplateTOML` 的回退语义、`GET /api/admin/releases` 返回空串的既有语义都不用另加状态。

契约与设计同步：`../CONTRACT.md` 1.5 明确写了这一条（含 `GET /api/admin/releases` 仍返回空串、不返回内置模板）；[DESIGN.md:67](../../DESIGN.md#L67) 的文件列表标注「不存在时用内置模板」。前端默认模板页原本就拦空保存（提示「留空保存不会改动现有模板」），UI 行为不变，这个能力通过 API 使用。

**验证**：回归 [api/api_test.go:1398](../../api/api_test.go#L1398) `TestBlankTemplateRestoresBuiltin`：先存自定义模板（文件存在），分别用 `""` 与 `"   \n\t\n"` 保存 → 200、`default_toml=""`、文件不存在、`GET /api/admin/template` 返回内置模板；第二次前重新存回自定义模板，证明两种空白一致。

## F6（P3）校验覆盖

- [api/regress_test.go](../../api/regress_test.go) 的管理面清单补齐 3 条：`POST`/`DELETE /api/admin/devices/{id}/credential`、`DELETE /api/admin/releases/{platform}`。清单现在 12 条，覆盖 `api.go` 注册的全部管理路由。
- [.github/workflows/build-and-push-docker-image.yml:37](../../.github/workflows/build-and-push-docker-image.yml#L37) 在 `go vet` 前加 gofmt 步骤（判断与 justfile 相同：`test -z "$(gofmt -l .)"`，失败时打印未格式化文件）。

**验证**：`TestManagementRoutesRequireSession` 12 条带伪造 bearer 全部 401；本地 `gofmt -l .` 为空（即新门禁通过）；CI 本身未跑（不推 git、不触发 workflow）。

## 未修 / 待确认

- **U1 一次性迁移未动**（[db/db.go:152](../../db/db.go#L152) `renameAppHashColumn`、[:172](../../db/db.go#L172) `dropReleaseHashColumn`、[:189](../../db/db.go#L189) `dropLegacySchema`）：按任务要求先不动。理由同 REVIEW——删掉后任何未迁移过的备份库都无法再启动，是否删除需要部署方确认线上/备份库都已迁移。
- **U2 `app_update.sha256` 的 `omitempty` 未动**：同属「旧数据兼容」待确认项，不在 F1–F6 范围内；去掉它会改 `../CONTRACT.md` 1.5 的客户端容忍约定，留给部署方与产品口径确认。
- U3–U7 未涉及（探针约定、`X-Forwarded-Proto` 已随 F2 消失、`default_toml` 已随 F5 定义、包内容校验已随 F1 补上、启动器自更新不在本仓库）。

## 构建与测试（实测）

环境：Windows + pwsh 7；`C:\Program Files\Go\bin` 前置到 PATH；`GOPROXY=https://goproxy.cn,direct`；`pnpm` 用 `C:\Users\11717\AppData\Roaming\npm\pnpm.cmd`。

| 命令 | 结果 |
| --- | --- |
| `gofmt -l .` | 空 |
| `go vet ./...` | exit 0 |
| `go test ./... -count=1` | exit 0：`api 9.316s`、`app 3.355s`、`builtin 0.955s`、`core 1.318s`、`db 3.243s`；其余包与探针无测试 |
| `pnpm --dir frontend typecheck` | exit 0 |

`Test*` 函数从 90 个增加到 **95** 个：新增 `TestCompareVersions`、`TestNeedsUpdate`、`TestHeartbeatOnlyUpgrades`、`TestClientPackageVersionComesFromThePackage`、`TestBlankTemplateRestoresBuiltin`；因语义按设计改变调整了 `TestDerivedStates`、`TestReleaseRoundTrip`、`TestAutoUpdateFollowsThePackage`、`TestHeartbeatCarriesLauncher`、`TestReleaseExposesFullDigest`、`TestPublishRejectsBothSlotsTogether`、`TestReleaseRejectsIncompletePublish`、`TestManagementRoutesRequireSession` 与夹具 `releasePkg`。

**未自动验证**：CI workflow 未实际触发；没有连线上种子/容器；没有用真实客户端跑完整的自动更新链路（客户端侧降级防护不在本仓库）。

## 收尾

- 改动文件：
  - 生产代码：`api/device_api.go`、`api/devices.go`、`api/settings.go`、`api/url.go`（删除）、`app/app.go`、`app/credential.go`、`app/device.go`、`app/settings.go`、`app/version.go`（新增）、`db/device.go`、`db/release.go`、`internal/testenv/testenv.go`。
  - 测试：`api/api_test.go`、`api/regress_test.go`、`app/device_test.go`、`app/version_test.go`（新增）、`db/group_test.go`。
  - 前端：`frontend/src/api.ts`、`frontend/src/components/UpdatesView.vue`。
  - 文档/门禁：`DESIGN.md`、`justfile`、`.gitignore`、`.github/workflows/build-and-push-docker-image.yml`；mesh 根目录的 `CONTRACT.md`。
- `CONTRACT.md` 在 mesh 根目录，**不在任何 git 仓库里**（`server/` 是独立仓库），所以这部分改动不会出现在 `server/` 的提交中。
- 本机 `server/dist/` 的旧产物处理见 F3 的注意；`.git/info/exclude` 只影响本机。
- `../client` 仓库在我开始时就有未提交改动（`README.md`、`internal/engine/update.go`、`internal/engine/update_test.go`），不是我改的，我没有触碰。
- 按要求**未 commit、未 push、未部署**，工作区保留全部改动等待审查。

