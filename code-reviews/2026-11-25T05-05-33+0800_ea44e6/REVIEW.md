# mesh-server 服务端评审（全量，HEAD ea44e63）

- **仓库**：`S:/local/repos/dsh_playground/mesh-app/server`（独立 git 仓库，module `mesh-server`）
- **评审 commit**：`ea44e635f7d6afd52cbada0866c807da6b1c8089`（short `ea44e63`，`main`）
- **评审开始时间**：2026-11-25 05:05:33 +08:00（目录名 `2026-11-25T05-05-33+0800_ea44e6`）
- **工作区状态（评审开始时）**：`git status --short` 输出为空（干净，无脏文件需要排除）。评审结束时只有 `?? code-reviews/`（本目录，未跟踪）。评审期间**没有修改任何生产代码、测试代码、构建脚本或文档**，没有 commit、没有 push。
- **评审范围**：`ea44e63` 的全部代码——后端 `main.go`、`web.go`、`api/`、`app/`、`core/`、`db/`、`httpx/`、`builtin/`、`internal/testenv/`；前端 `frontend/src`（只审安全面：XSS、凭证存放、CSRF、URL 回显）；构建与部署 `justfile`、`Dockerfile`、`Dockerfile.runtime`、`k8s.mesh-server.yaml`、`.gitignore`/`.dockerignore`。`frontend/dist`、`.dist/`、`dist/`、`data/` 是构建/运行产物，不作为源码评审。
- **对照文档**：`../CONTRACT.md` 第 1 节（存在，按"以它为准"逐条对照）、`DESIGN.md`（实现与部署）、`README.md`、`justfile`。
- **方法**：通读全部源码与文档 → 逐条比对 CONTRACT.md 1.3/1.4/1.5 与实现 → 6 个探针脚本（`scripts/`，全部可重跑）复现可疑点 → 真实构建 + `httptest`/临时端口冒烟。
> 约束遵守说明：未部署、未重启任何服务、未连线上主机（ali/tencent/zyf）、未改数据库、未动本机正在运行的 mesh 进程；所有探针都用临时 `DATA_DIR` + `httptest`，临时目录用后即删。

## 结论摘要

| 级别 | 数量 | 说明 |
| --- | --- | --- |
| P0 | 1 | 生产凭证（AUTH_TOKEN / SESSION_SIGNING_KEY / READYZ_TOKEN）明文随 `k8s.mesh-server.yaml` 入库并已推到两个远端 |
| P1 | 1 | `state.json` 每次启动重导：撤销的设备、删除的发布在重启后复活，只剩启动器的发布被旧数据覆盖 |
| P2 | 6 | 双槽位发布非原子（400 也算部分生效）；`/api/*` 无读超时被未认证慢请求占满；nonce 重放窗口在时钟偏移下失效；无发布时 `app_state` 与契约相反；`just check` 的 gofmt 门禁在 HEAD 必然失败；接口/表结构文档漂移 |
| P3 | 5 | 405 的 `Allow` 头恒定错误；前端默认模板是过期副本；发布 URL 无 SSRF 防护；死代码；只为测试存在的安全开关 |

最关键的是 F1（凭证入库）与 F2（删除不持久）：前者是可直接使用的管理面全权凭证，后者让"撤销设备"在下次重启后自动失效。其余 P2 都是可复现的行为偏差；其中 F4 在生产上会被 nginx 部分掩盖，但应用层确实没有防线。

## 1. 上一轮修复核验

按要求先执行"核验上一轮修复"，但本仓库**没有上一轮评审基线**：

- `git log -- code-reviews` 与 `git ls-files | Select-String code-reviews` 都是空（17 个提交里 `code-reviews/` 从未出现）；
- `git log --oneline --all | Select-String "评审|review|REVIEW"` 也是空；
- 工作区里确实存在 `code-reviews/` 历史，但它在 `mesh-app/client/`（另一个 git 仓库、另一个 module `mesh/client`，两次评审 `2026-11-24T17-50+0800_f38b06e`、`2026-11-24T20-52-34+0800_9efdcc` 及其修复 `31afeae`、`39959ab`），审的是 Windows 客户端，不覆盖服务端任何文件。

因此本轮按**全量首轮**执行：不核验上轮条目，只审当前代码；编号从 `F1` 开始，没有"上轮-Fn"。

## 2. 本轮发现

### [P0][F1] 生产凭证明文入库：`k8s.mesh-server.yaml` 里的 AUTH_TOKEN / SESSION_SIGNING_KEY / READYZ_TOKEN

**位置**：[k8s.mesh-server.yaml:26](../../k8s.mesh-server.yaml#L26)（`AUTH_TOKEN`）、[k8s.mesh-server.yaml:28](../../k8s.mesh-server.yaml#L28)（`SESSION_SIGNING_KEY`）、[k8s.mesh-server.yaml:29](../../k8s.mesh-server.yaml#L29)（`READYZ_TOKEN`）、[k8s.mesh-server.yaml:97](../../k8s.mesh-server.yaml#L97)（readinessProbe 把同一个 READYZ_TOKEN 拼进 URL）。

**触发条件**：任何人拿到这个仓库（`git remote -v` 有两个远端：`origin` = github.com/hiromuraki/mesh-server、`local` = git.daveny.cn/hiromuraki/mesh-server），或读到任意一次 clone 出来的副本。**不需要任何权限**。

**实际结果**：三个值不是占位符——`READYZ_TOKEN` 同时出现在 readinessProbe 的 `?token=` 里，说明这份清单就是直接 `podman kube play` 用的部署清单，值即线上值。三者的作用分别是：`AUTH_TOKEN` 可直接换 24 小时 Bearer 拿到整个管理面（设备配置、含 `network_secret` 的 TOML、发布地址、一次性注册令牌）；`SESSION_SIGNING_KEY` 可离线伪造任意 Bearer（不需要知道 AUTH_TOKEN），轮换它本来等于吊销全部凭证，泄漏后等于凭证永不过期；`READYZ_TOKEN` 可探测内部 `/readyz`。三个值还留在 git 历史里（`f99db85`、`2d05902`、`63f7005` 三次提交都含该文件）。

**修复建议**：

1. 先把三个值全部轮换（AUTH_TOKEN、SESSION_SIGNING_KEY、READYZ_TOKEN），把"删文件"当成止血、把"轮换"当成修复；
2. 把 Secret 拆到不入库的文件（例如 `k8s.mesh-server.secret.yaml`，写进 `.gitignore`，用 `podman kube play` 时显式指定，或改用 `podman secret`/外部 env 文件）；仓库里的 `k8s.mesh-server.yaml` 只留 ConfigMap 与非敏感字段；readinessProbe 的 token 也不要写死在清单里；
3. `.dockerignore` 已经排掉 `k8s.*`，但 `.gitignore` 没有排任何 secret 文件，建议加 `*.secret.yaml`；
4. 历史里的旧值必须按"已泄漏"处理（轮换即可，除非要求清理历史）。

### [P1][F2] `state.json` 每次启动都重新导入：删除的设备与发布会"复活"，只剩启动器的发布会被旧客户端包覆盖

**位置**：[app/app.go:64](../../app/app.go#L64)（`Open` 每次启动都调 `importLegacyState`）→ [app/migrate.go:54](../../app/migrate.go#L54)（没有任何"已迁移"判断）、[app/migrate.go:92](../../app/migrate.go#L92)（`ImportDevices`）、[app/migrate.go:98](../../app/migrate.go#L98)（`ImportRelease`）；[db/device.go:281](../../db/device.go#L281)-[db/device.go:303](../../db/device.go#L303)（`ON CONFLICT DO NOTHING`，但"行不存在"被当成"没迁过"）、[db/release.go:115](../../db/release.go#L115)-[db/release.go:130](../../db/release.go#L130)（只有 `found && existing.Version != ""` 才跳过，命中后用 `SetRelease` 整行覆盖）、[db/release.go:155](../../db/release.go#L155)-[db/release.go:181](../../db/release.go#L181)（`setting` 表同款逻辑）。

**触发条件**：`DATA_DIR` 里存在 `state.json`（从 JSON 版升级来的宿主机都有；本仓库 `data/state.json` 里就有 1 台设备），并且管理员在管理页删过设备或删过发布。重启即触发。

**实际结果**（探针 `scripts/probe_legacy_resurrect`）：

```
AFTER FIRST START  devices=1 release_version="2026.11.1"
AFTER DELETE      devices=0 release_found=false version=""
AFTER RESTART     devices=1 release_found=true version="2026.11.1" hash="0123456789abcdef"
LAUNCHER PUBLISHED version="" launcher_version="2026.11.25-9" launcher_url="http://127.0.0.1:63486/mesh.exe"
AFTER RESTART     version="2026.11.1" hash="0123456789abcdef" launcher_version="" launcher_url=""
REPRODUCED: the deleted device and the deleted release came back, and the launcher-only release was overwritten
```

三个后果，同一根因：

1. **撤销设备失效**：`DELETE /api/devices/{id}` 删掉行，重启后 `ImportDevices` 用 `state.json` 里的 `pubkey`/`device_id`/`config_toml` 原样插回。设备行重新存在 → `deviceAuth` 查得到 → Ed25519 验签通过，被撤销的设备又能取走含 `network_secret` 的配置。删除时写的 `revoked` 墓碑只在内存，重启本来就会丢；现在连"设备不存在"这条兜底也没了。
2. **删除发布失效**：`DELETE /api/admin/releases/{platform}` 让 `release` 表空出来，重启后 `importLegacyState` 认为"没迁过"，把 `state.json` 里的旧客户端包重新写入。管理员以为下架了，客户端心跳又会收到 `app_update`。
3. **只剩启动器的平台被覆盖**：`SetLauncherRelease` 建出的行 `version` 为空，正好满足 `ImportRelease` 的"该平台还没发布客户端包"，于是 `SetRelease` 整行覆盖——旧客户端包回来、刚发布的启动器三列被清空。`launcher_only` 的发布在每次重启都会被抹掉。

`DESIGN.md` 第 5 节写的意图是"已存在的行一律跳过，所以迁移之后再在管理页改过的设备不会被旧文件覆盖"，实现只覆盖了"改"，没覆盖"删"。

**修复建议**：把迁移改成**一次性**，不要用"表里有没有行"当判据：

- 在 schema 里加一张 `schema_meta(key TEXT PRIMARY KEY, value TEXT)`（或复用 `setting` 表加一行标记），首次成功导入后写入 `legacy_imported_at`，之后再也不读 `state.json`/`setting`；
- 迁移与标记放在同一事务里，中途失败不留半套；
- 如果确实需要"每次启动都补齐旧记录"，就必须记录删除墓碑（`deleted_legacy_id`），否则删除语义永远不成立；
- 回归测试：导入 → 删除设备/发布 → 重新 `app.Open` → 断言仍然是空的。

### [P2][F3] 双槽位发布不是原子操作：响应 400 时客户端槽位可能已经生效

**位置**：[api/settings.go:115](../../api/settings.go#L115)-[api/settings.go:137](../../api/settings.go#L137)（先 `SetClientExternal`、再 `SetLauncherExternal`、最后 `SaveDefaultTOML`，每步失败就 `return`，没有任何回滚）、[app/settings.go:107](../../app/settings.go#L107)-[app/settings.go:129](../../app/settings.go#L129)（先写库）、[app/settings.go:134](../../app/settings.go#L134)-[app/settings.go:148](../../app/settings.go#L148)（后校验/下载）；同类：[db/release.go:142](../../db/release.go#L142)-[db/release.go:151](../../db/release.go#L151)（`ClearLauncher` 的 UPDATE 与 DELETE 是两条独立语句，不是事务）。

**触发条件**：`PUT /api/admin/releases` 一次发布两个槽位，而第二个槽位校验失败或下载失败（URL 不可达、超 200MB、非 200）。或者两个管理员并发发布/清除同一平台。

**实际结果**（探针 `scripts/probe_publish_atomicity`）：

```
PUBLISH status=400 body={"code":"invalid_argument","message":"无法读取 launcher_url 指向的文件：...connectex: ..."}
AFTER platform=windows/amd64 app_version="2026.11.25-1" app_hash="efe0e1d322d28c45" launcher_version=""
REPRODUCED: answered 400 but the client slot is published
```

客户端槽位已经写进库、`app_hash` 已经算好，下一次任何该平台设备心跳都会拿到 `app_update`；而管理页拿到的是 400，界面提示"发布失败"，管理员无从知道半个发布已经生效。`default_toml` 也在两次写库之后，写文件失败返回 500 时两个槽位同样已经生效。

`ClearLauncher` 的并发窗口：A 执行 UPDATE 把启动器三列清空 → B 的 `SetLauncherRelease` 重新发布（`version` 仍为空，因为 B 只写启动器）→ A 执行 `DELETE ... AND version 为空`，把 B 刚发布的启动器连同整行删掉。

**修复建议**：把"校验 + 下载 + 落库"拆成两个阶段——先在内存里把两个槽位的 `(version,url,hash,sha256,notes)` 全部算完（任一失败直接 400，什么都不写），最后用一个事务同时写两个槽位（`db` 层新增 `SetBothSlots`）；`default_toml` 的文件写入放在事务前后都行，但失败时必须回滚事务或明确告诉调用方"哪些已生效"；`ClearLauncher` 用单事务（UPDATE 与 DELETE 放进同一个 `tx`）。

### [P2][F4] `/api/*` 没有读超时：一条不发送请求体的未认证连接就能占满共享并发门，整个控制台 API 返回 503 busy

**位置**：[main.go:113](../../main.go#L113)-[main.go:117](../../main.go#L117)（`http.Server` 只设了 `ReadHeaderTimeout`，没有 `ReadTimeout`/`IdleTimeout`）、[httpx/response.go:43](../../httpx/response.go#L43)-[httpx/response.go:45](../../httpx/response.go#L45)（`ReadBody` 直接 `io.ReadAll`，没有截止时间）、[api/api.go:145](../../api/api.go#L145)-[api/api.go:146](../../api/api.go#L146)（`/api/register`、`/api/heartbeat` 与所有管理接口共用 `apiGate`）、[api/api.go:173](../../api/api.go#L173)-[api/api.go:183](../../api/api.go#L183)（门满即 503，不排队）。

**触发条件**：未认证连接发出 `POST /api/heartbeat` 的请求头，声明 `Content-Length: N`，然后一个字节都不再发。不需要任何凭证。

**实际结果**（探针 `scripts/probe_slow_body`，为便于复现把门设成 1 个槽位；部署默认 `MAX_API_CONCURRENCY=128`）：

```
WHILE-BLOCKED GET /api/devices status=503 body={"code":"busy"}
AFTER-CLOSE   GET /api/devices status=200 body=[]
REPRODUCED: one idle unauthenticated request made the console API answer 503 while it was open
```

挂住的 handler 一直停在 `io.ReadAll`，`defer gate.Release()` 永远不执行；由于设备接口与管理接口共用同一扇门，128 条这样的连接就能让 `GET /api/devices`、`PUT /api/admin/releases` 全部 503。`/api/auth` 有独立门（32）所以还能登录，但登录后什么都读不到；`/livez`、`/readyz` 不受影响，探针照样绿。

**修复建议**：至少加一条覆盖请求体读取的截止时间——`http.Server{ReadHeaderTimeout: 10s, ReadTimeout: 15s, IdleTimeout: 60s}`，或在 handler 入口 `http.NewResponseController(w).SetReadDeadline(...)`（对慢上传更精确）；同时把设备面与管理面拆成两个 `Gate`（设备面被灌满时不该影响控制台）。


### [P2][F5] nonce 缓存 TTL 与时间戳窗口不匹配：客户端时钟偏快时，已用过的签名可以在时间窗内重放

**位置**：[api/api.go:84](../../api/api.go#L84)（`core.NewNonceCache(core.ClockSkew)`）、[core/nonce.go:30](../../core/nonce.go#L30)（过期时间 = **服务器看到它的时刻** + TTL）、[core/signing.go:33](../../core/signing.go#L33)-[core/signing.go:46](../../core/signing.go#L46)（接受 `|now-ts| <= 300s`，即签名时间戳最多可在未来 300s）、[api/device_api.go:89](../../api/device_api.go#L89)（验签后 `Claim`）。

**触发条件**：设备时钟比服务器快（签名时间戳 `ts` 落在服务器未来），请求在窗口边缘被接受；攻击者录下这条请求，等 nonce 记录过期、但 `ts` 仍在时间窗内时重放。

**实际结果**（探针 `scripts/probe_nonce_window`）：

```
FIRST  now=2024-11-23T22:13:20Z ts=1732400300 timestampErr=<nil> claimed=true
REPLAY now=2024-11-23T22:18:21Z timestampErr=<nil> claimed=true
REPRODUCED: the replayed signature is accepted as unused although it was already used
```

推导：首次到达时 `now1 = ts-300`，nonce 记录到 `now1+300 = ts` 过期；在 `now2 = ts+1` 重放，`CheckTimestamp` 看到 `|ts-now2| = 299` 通过，`Claim` 看到记录已过期返回 true。也就是说重放窗口最多可以比 nonce 记录多活 300 秒。后果不是"立刻能伪造"（仍需一条真实签名），但可以让攻击者重放旧心跳：把 `last_config_hash`/`last_app_hash`/`last_platform` 写回旧值，让控制台以为设备在线、并触发多余的配置重推。

**修复建议**：nonce 的过期时间取 `max(now, ts) + ClockSkew`，或直接把 TTL 设为 `2*ClockSkew`（覆盖"未来 300s 才被接受"的极值）；也可以先 `Claim` 再验签，但那样就给了未认证方灌缓存的能力，不建议。

### [P2][F6] 没有发布时 `app_state` 对"未上报平台"的设备返回 `stale`，与 CONTRACT 1.4 相反

**位置**：[app/device.go:299](../../app/device.go#L299)-[app/device.go:311](../../app/device.go#L311)（先判 `d.LastPlatform == ""` → 直接 `stale`，之后才看 `r.Hash == ""`）、契约 [CONTRACT.md:122](../../../CONTRACT.md#L122)（"没有发布（`app_hash` 为空）时也算 `current`，因为不存在可更新到的版本"）。

**触发条件**：刚创建、还没注册/上报过 platform 的设备（`last_platform` 为空）——`GET /api/devices` 里的 `app_state`。

**实际结果**（探针 `scripts/probe_app_state`）：

```
DEVICE id=d_3f33cb status=pending last_platform="" app_state=stale app_version=""
REPRODUCED: nothing is published, CONTRACT.md 1.4 says app_state is current, server says stale
```

控制台把一台全新的设备显示成"待更新"（[frontend/src/format.ts:66](../../frontend/src/format.ts#L66)-[frontend/src/format.ts:69](../../frontend/src/format.ts#L69)），而按契约此时根本没有可更新到的版本。注释里给的理由（"平台未知说明还没上报过，不可能已经在跑对应平台的包"）只对"已经发布了该平台"的情况成立。

**修复建议**：按契约的顺序判定——`r.Hash == "" → current` 优先，然后再处理平台未知；如果确实要区分"平台未知"，就改 `CONTRACT.md` 1.4 并同步客户端，不要只改实现。

### [P2][F7] `just check` / `just test` 在 HEAD 必然失败：两个文件没通过 gofmt

**位置**：[api/device_api.go:301](../../api/device_api.go#L301) 与 [api/api_test.go:1197](../../api/api_test.go#L1197) 都缺少文件末尾换行（`\ No newline at end of file`）；门禁在 [justfile:47](../../justfile#L47)（`test -z "$(gofmt -l .)"` 不成立就 `exit 1`），`check: vet test` 依赖它。

**触发条件**：`just check` 或 `just test`（这也是 CONTRACT.md 第 3 节列的服务端验收命令）。

**实际结果**：`gofmt -l .` 输出 `api/api_test.go`、`api/device_api.go`，于是 `just test` 在格式化门禁处 `exit 1`，`go vet`/`go test` 根本不会执行——验收清单第一项"`just check` 全绿"在 HEAD 上无法达成。`gofmt -d` 的差异只有末尾换行，没有任何语义改动。本机没有装 `just`（`Get-Command just` 为空），我用等价命令复现了同一条门禁（见第 4 节）。

**修复建议**：`gofmt -w api/device_api.go api/api_test.go`（并在编辑器里打开"文件末尾插入换行"，避免再犯）。建议在 CI 或 pre-commit 里跑同一条命令，而不是只靠"提交前记得跑"。

### [P2][F8] 新增的分组/布局接口没有写进 CONTRACT.md，`group_id`/`position`/`last_platform`/`device_group` 也没写进 DESIGN 的表结构

**位置**：实现 [api/api.go:127](../../api/api.go#L127)（`GET /api/template`）、[api/api.go:134](../../api/api.go#L134)-[api/api.go:140](../../api/api.go#L140)（分组与布局）、[db/db.go:33](../../db/db.go#L33)-[db/db.go:34](../../db/db.go#L34)（`group_id`,`position`）、[db/db.go:37](../../db/db.go#L37)-[db/db.go:41](../../db/db.go#L41)（`device_group`）；文档 [CONTRACT.md:94](../../../CONTRACT.md#L94)-[CONTRACT.md:110](../../../CONTRACT.md#L110)、[DESIGN.md:29](../../DESIGN.md#L29)-[DESIGN.md:40](../../DESIGN.md#L40)、[DESIGN.md:57](../../DESIGN.md#L57)-[DESIGN.md:102](../../DESIGN.md#L102)。

**触发条件**：按契约实现/联调（CONTRACT.md 第 3 行明确规定"任何一方改动接口，必须同步改本文件"）。

**实际结果**：服务端已有、但 CONTRACT 1.4 表格里没有的接口共 7 条：

| 方法 | 路径 | 认证 |
| --- | --- | --- |
| GET | `/api/template` | Bearer |
| PUT | `/api/devices/{id}/group` | Bearer |
| PUT | `/api/layout` | Bearer |
| GET / POST | `/api/groups` | Bearer |
| PUT / DELETE | `/api/groups/{id}` | Bearer |

`GET /api/devices` 的响应还多出 `group_id`、`position` 两个字段（[api/devices.go:27](../../api/devices.go#L27)-[api/devices.go:28](../../api/devices.go#L28)），`GET /api/devices/{id}` 同样多出这两个字段（[api/devices.go:87](../../api/devices.go#L87)-[api/devices.go:88](../../api/devices.go#L88)）；CONTRACT 1.1 的设备列清单没有 `group_id`/`position`，也没有 `last_platform`（`last_platform` 只在 1.4 的响应字段里出现过一次，1.1 的列说明缺失）；同样没有 `device_group` 表。DESIGN §3 的路由表少了上表后 4 条（template 已列），§4 的表结构块（自称"`db/db.go` 是唯一的 SQL 位置"）与 `db/db.go` 对不上：少了 `last_platform`、`group_id`、`position` 三列，缺整张 `device_group` 表（`grep last_platform|group_id|position|device_group DESIGN.md` 零命中）。

后果不是运行期错误，而是**契约失真**：客户端或其他实现按 CONTRACT 写，会漏掉分组能力、也解析不到 `group_id`/`position`；下一轮改动无法判断"哪些字段是契约的一部分"。

**修复建议**：补齐 CONTRACT.md 1.4 的路由表与字段说明（含错误码 `group_not_found`）、1.1 的列与 `device_group` 表；DESIGN §3/§4 直接照 `db/db.go` 的 schema 重写。


### [P3][F9] 405 的 `Allow` 头恒定是 `GET, HEAD`，与实际方法不符

**位置**：[api/middleware.go:89](../../api/middleware.go#L89)-[api/middleware.go:93](../../api/middleware.go#L93)（`methodNotAllowed` 写死 `w.Header().Set("Allow", "GET, HEAD")`）、[api/api.go:157](../../api/api.go#L157)-[api/api.go:165](../../api/api.go#L165)（`route()` 每个路径只注册一个不带方法的兜底，没有记录已注册的方法）。

**触发条件**：对任意已注册路径用不支持的方法请求。

**实际结果**（探针 `scripts/probe_allow_header`）：

```
/api/auth                real methods: POST only  GET -> 405 Allow="GET, HEAD"
/api/admin/releases      real methods: GET, PUT   POST -> 405 Allow="GET, HEAD"
/api/devices             real methods: GET, POST  DELETE -> 405 Allow="GET, HEAD"
/api/layout              real methods: PUT only   GET -> 405 Allow="GET, HEAD"
```

对 `GET /api/auth` 这种"只有 POST"的路径，`Allow` 反过来声明 GET 可用，属于误导（RFC 9110 要求 `Allow` 列出真实支持的方法）。既有测试只断言 `DELETE /api/devices` 的 `Allow` 含 `GET`，正好被这条正确情况掩盖。

**修复建议**：在 `route()` 里按路径维护"已注册方法"的集合，405 时输出真实列表（去重，`GET` 存在时补 `HEAD`）；顺带让 fallback 注册与真实方法集合在同一处维护。

### [P3][F10] 前端 `FALLBACK_TOML` 是默认模板的过期副本：模板接口失败时新建设备会写入固定 IP、且缺 `[flags]`

**位置**：[frontend/src/format.ts:3](../../frontend/src/format.ts#L3)-[frontend/src/format.ts:17](../../frontend/src/format.ts#L17)（`ipv4 = "10.126.126.2/24"`，无 `[flags]`）、[frontend/src/components/NewDeviceDialog.vue:17](../../frontend/src/components/NewDeviceDialog.vue#L17)（初始值就用它）、[frontend/src/components/NewDeviceDialog.vue:24](../../frontend/src/components/NewDeviceDialog.vue#L24)-[frontend/src/components/NewDeviceDialog.vue:31](../../frontend/src/components/NewDeviceDialog.vue#L31)（`api.template()` 成功才覆盖，失败静默保留兜底）、[builtin/peer.default.toml:1](../../builtin/peer.default.toml#L1)-[builtin/peer.default.toml:19](../../builtin/peer.default.toml#L19)（真正的内置模板：`dhcp = true` + `[flags] encryption_algorithm`）。

**触发条件**：`GET /api/template` 失败或还没返回（401、代理 502、刚打开对话框就点"生成令牌"）。

**实际结果**：三份默认模板已经分叉——`builtin/peer.default.toml`（dhcp）、`DATA_DIR/peer.default.toml`（若存在，唯一事实来源）、`frontend/src/format.ts` 的 `FALLBACK_TOML`（固定 `.2` 地址、无加密算法段）。用户在这种情况下创建的设备会拿到 `ipv4 = 10.126.126.2/24`，第二台同类设备直接撞地址（模板注释第 1 行警告的正是这件事），并且缺少全网的 `encryption_algorithm` 段。

**修复建议**：删掉前端副本，初始值留空并在模板到达后填入；模板取不到时禁用"生成令牌"并提示"读取默认模板失败，请重试"。默认模板只能有一个事实来源。

### [P3][F11] 发布地址可以指向任意地址：服务端会真的去 GET，无 SSRF 防护

**位置**：[app/settings.go:169](../../app/settings.go#L169)-[app/settings.go:187](../../app/settings.go#L187)（`validateSlot` 只校验 `http(s)` 与非空 host）、[app/settings.go:191](../../app/settings.go#L191)-[app/settings.go:210](../../app/settings.go#L210)（`fetchDigest` 用默认 `http.Client`，自动跟随最多 10 次重定向，不限制目标地址）。

**触发条件**：管理员把 `app_url`/`launcher_url` 填成内网地址（`127.0.0.1`、控制面自己所在的 `10.89.2.0/24`、云 metadata `169.254.169.254`）或一个会 302 到内网的公网地址，然后点保存。

**实际结果**：控制面容器会替调用方发起请求，结果只用于算 hash，但错误信息会原样回显给管理员（`无法读取 app_url 指向的文件：Get "http://…": dial tcp …`，本报告的 F3 复现输出里就能看到这种回显），足以做内网端口/服务探测；重定向不受约束，可以把"看起来正常的外部地址"跳到内网。这不是越权（管理员本来就是最高权限），但把"只读管理面"变成了"可打内网的 HTTP 客户端"，在控制面与数据库/内网同网段的部署里放大了管理员凭证的价值。

**修复建议**：`fetchDigest` 里用自定义 `Transport.DialContext` 在连接前解析并拒绝回环/私网/链路本地/组播地址（`netip.Addr.IsPrivate`/`IsLoopback`/`IsLinkLocalUnicast`），`CheckRedirect` 对每一跳重新校验；或把"服务端下载"改成"管理员上传文件 / 填写 sha256"，彻底去掉出站请求。

### [P3][F12] 死代码与冗余分支

**位置与实际情况**：

- [builtin/builtin.go:9](../../builtin/builtin.go#L9)-[builtin/builtin.go:19](../../builtin/builtin.go#L19)：`Asset`/`Manifest` 全仓没有任何引用（grep `Manifest` 只命中定义处）。注释说"让运维看到构建带了什么"，但没有任何入口暴露它。
- [app/app.go:90](../../app/app.go#L90)：`func (a *App) DataDir()` 无调用者。
- [frontend/src/format.ts:37](../../frontend/src/format.ts#L37)-[frontend/src/format.ts:41](../../frontend/src/format.ts#L41)：`statusRank` 无调用者（设备排序已改成 `position` + 创建顺序）。
- [frontend/src/api.ts:116](../../frontend/src/api.ts#L116)-[frontend/src/api.ts:129](../../frontend/src/api.ts#L129)：`decode()` 里 `if (message) throw new ApiError(res.status, code);` 与紧随其后的 `throw new ApiError(res.status, code);` 完全相同，`message` 解析出来就丢掉了。

**修复建议**：删掉 `Manifest`/`Asset`/`DataDir`/`statusRank`；`decode` 要么把 `message` 真的用进错误文案（现在只有 `releasesRequest` 用了），要么退回一行 `throw new ApiError(res.status, code)`。

### [P3][F13] `MinAuthDelay` 是只为测试存在的生产安全开关

**位置**：[api/api.go:40](../../api/api.go#L40)-[api/api.go:42](../../api/api.go#L42)（`Options.MinAuthDelay`，注释写着 "Zero disables it, which is what the tests use"）、[main.go:111](../../main.go#L111)（生产恒 `time.Second`）、[internal/testenv/testenv.go:96](../../internal/testenv/testenv.go#L96)（测试置 0）。

**触发条件**：任何新的调用方（新测试、新入口、将来复用 `api.New` 的代码）不传这个字段，就静默拿到"关闭登录节流"的弱安全默认值。

**实际结果**：登录最小延迟（1s + rand(200ms)）是防时序探测与限速的一部分，却做成了公开可关的开关，唯一理由是省测试时间（`api` 包测试总共 7.1s，其中一部分就是十几次登录）。这违反项目自己的约定"不应仅为测试便利而向生产代码引入无实际业务价值的状态、接口或特殊分支"。

**修复建议**：把下限做成 `api` 包内常量；测试要快就注入可替换的 `sleep func(time.Duration)`（与 `app.Options.Now` 同一思路），或接受测试多花几秒。安全参数不要出现在公开 `Options` 里。

## 3. 待确认（非 finding）

以下问题要么影响面依赖部署环境，要么我在不改数据/不起服务的前提下无法复现，只记录观察：

- **U1 `absoluteURL` 的信任模型与 `ClientIP` 不一致**：[api/url.go:24](../../api/url.go#L24)-[api/url.go:31](../../api/url.go#L31) 无条件采信任意来源的 `X-Forwarded-Proto` 与 `r.Host`，而 [httpx/clientip.go:41](../../httpx/clientip.go#L41)-[httpx/clientip.go:67](../../httpx/clientip.go#L67) 只信任 `TRUSTED_PROXIES`。当前发布地址经 `validateSlot` 保证是绝对地址，`absoluteURL` 原样返回，所以这条路径对现有数据不可达；只有迁移进来、`url.Parse` 认为是"相对"的旧地址才会拼上调用方可控的 Host/Proto。需要确认迁移数据里是否存在相对地址。
- **U2 迁移进来的 `app_url` 没过 `validateSlot`**：[app/migrate.go:98](../../app/migrate.go#L98) 直接把 `state.json` 的 `app_url` 交给 `ImportRelease`（[db/release.go:126](../../db/release.go#L126)），没有 scheme/host 校验，最终会原样出现在心跳的 `app_update.url` 里。能否触发取决于旧 `state.json` 的内容；本机 `data/state.json` 没有 `settings` 字段，无法复现。
- **U3 心跳字段没有长度上限**：`heartbeatRequest` 的 `platform`/`configHash`/`appHash`（[api/device_api.go:44](../../api/device_api.go#L44)-[api/device_api.go:52](../../api/device_api.go#L52)）只受 1MiB 请求体上限约束，`RecordHeartbeat` 会把它们原样写进 `last_platform`/`last_config_hash`/`last_app_hash`（[db/device.go:253](../../db/device.go#L253)）。有设备私钥的一方能撑大数据库与页面内容；实际影响取决于客户端是否可能上报超长值，未构造真实设备验证。
- **U4 两个内存 map 没有上界**：`revoked`（[app/device.go:223](../../app/device.go#L223)-[app/device.go#L230](../../app/device.go#L230)）随删除次数增长、永不清理；`NonceCache.seen`（[core/nonce.go:30](../../core/nonce.go#L30)）随"认证设备 × 请求速率 × 300s"增长。量级都不大（单条几十字节），未做压测。
- **U5 关闭顺序**：`purgeTokens` goroutine（[main.go:128](../../main.go#L128)-[main.go:130](../../main.go#L130)）与 `application.Close()`（[main.go:159](../../main.go#L159)）之间没有同步，`defer stopPurge()` 在 `run()` 返回时才执行。退出瞬间 ticker 若触发，会在已关闭的数据库/日志上打一条错误。不可观测，未复现。
- **U6 没有 panic 恢复中间件**：[api/middleware.go:19](../../api/middleware.go#L19)-[api/middleware.go:42](../../api/middleware.go#L42) 只加安全头与访问日志。net/http 会按连接恢复 panic，进程不会挂（服务不中断），但该请求会以断连收场、访问日志里没有状态码。是否要显式 500 + 日志，取决于运维偏好。
- **U7 生产环境的 nginx 可能掩盖 F4**：`DESIGN.md` 第 1 节写"反向代理负责终止 TLS、限速与域名"，`k8s.mesh-server.yaml:13` 只信任 `10.89.2.0/24`。若 nginx 默认缓冲请求体（`proxy_request_buffering on`），慢 body 到不了应用；但集群内直连 `hostPort 8088`（`k8s.mesh-server.yaml:74`-`:78`）的路径没有这层保护。需要在部署环境确认哪条路径对外可达。
- **U8 前端凭证存放（安全面复核结论）**：Bearer 存在 `sessionStorage`（[frontend/src/api.ts:72](../../frontend/src/api.ts#L72)-[frontend/src/api.ts:81](../../frontend/src/api.ts#L81)），不是 cookie，所以 CSRF 不成立（浏览器不会自动带上它），也没有 HttpOnly/SameSite/Secure 可谈；代价是同源脚本可读。已确认前端**没有** `v-html`/`innerHTML`，`index.html` 无内联脚本，CSP（[api/middleware.go:14](../../api/middleware.go#L14)-[api/middleware.go:16](../../api/middleware.go#L16)）对 script 只放 `self`，包的下载地址只用 `{{ }}` 文本插值、没有变成 `:href`/`<a>`，不存在 `javascript:` 点击注入。这一项没有发现可报的问题。
- **U9 `window.__app` 调试入口在生产构建里**：[frontend/src/main.ts:9](../../frontend/src/main.ts#L9) 把根组件挂到 `window`（`env.d.ts` 里有类型声明）。只有同源脚本能访问，风险可忽略，但属于"调试代码随发布上线"，可考虑只在 `import.meta.env.DEV` 下挂。

## 4. 构建与测试

**环境**：Windows + pwsh 7；`go version go1.27.3 windows/amd64`；`pnpm 12.23.0`（`$env:APPDATA\npm\pnpm.cmd`）；`GOPROXY=https://goproxy.cn,direct`。本机**没有安装 `just`**（`Get-Command just` 为空；recipe 还需要 bash），所以按 `justfile` 逐条执行等价命令，并单独复现了它的门禁判断。

| 实际执行的命令 | 结果 |
| --- | --- |
| `gofmt -l .`（`justfile:47` 的门禁判断） | **失败**：列出 `api/device_api.go`、`api/api_test.go`（见 F7）。`gofmt -d` 差异只有文件末尾换行 |
| `go vet ./...` | 通过（exit 0） |
| `go test ./... -count=1` | 通过：`mesh-server/api 7.129s`、`mesh-server/app 3.002s`、`mesh-server/core 0.772s`、`mesh-server/db 2.329s`；`mesh-server`、`builtin`、`httpx`、`internal/testenv` 无测试文件 |
| `go build -o <临时目录>/mesh-server.exe .` | 通过（exit 0），产物已删除，工作区保持干净 |
| `pnpm typecheck`（`vue-tsc --noEmit`） | 通过（exit 0） |
| 冒烟：同一份二进制，临时 `DATA_DIR`，`HOST=127.0.0.1 PORT=18099`，`AUTH_TOKEN`/`SESSION_SIGNING_KEY`/`READYZ_TOKEN` 用探针值 | `/livez` 200 且**不被记日志**（符合 DESIGN §8）；`/readyz?token=…` 200；`/api/health` 200；`/admin` 200（485 字节 `index.html`）；`/` 302 → `/admin`；`/api/devices` 无凭证 401；`/api/devices` 带伪造 Bearer 401 `{"code":"unauthorized"}`；`/nope` 404；stdout 是 JSON 行日志，时间戳为 `+08:00` |

**探针脚本**（`scripts/`，`go run ./code-reviews/2026-11-25T05-05-33+0800_ea44e6/scripts/<名字>` 即可重跑，全部使用临时 `DATA_DIR` 与 `httptest`，不碰本机数据与端口）：

| 脚本 | 对应发现 | 结论 |
| --- | --- | --- |
| `probe_legacy_resurrect` | F2 | 复现：删除的设备与发布在重启后复活，launcher-only 行被覆盖 |
| `probe_publish_atomicity` | F3 | 复现：`400 invalid_argument` 但客户端槽位已生效 |
| `probe_slow_body` | F4 | 复现：一条未认证挂起请求让管理接口 503 `busy` |
| `probe_nonce_window` | F5 | 复现：时间窗内重放已用过的 nonce 被接受 |
| `probe_app_state` | F6 | 复现：无发布时新建设备 `app_state=stale` |
| `probe_allow_header` | F9 | 复现：4 条路径的 405 都返回 `Allow: GET, HEAD` |

**未跑/未验证项**：

- `just check` / `just test` 本身没跑（本机无 `just`；recipe 需要 bash）。已逐条跑等价命令，并单独执行了 gofmt 门禁判断，结论与 justfile 一致。
- `pnpm build` / `just frontend` 未跑：会重写 `frontend/dist`（被 `go:embed` 嵌入的构建产物），属于构建资产而非源码；类型检查已由 `pnpm typecheck` 覆盖。
- `just build` 的交叉编译（`linux/amd64`+`linux/arm64`）、`Dockerfile`/`Dockerfile.runtime` 构建、wails3/podman 打包按约束**不跑**。
- 优雅关闭未验证：`SIGINT → shutdown signal received / server shutdown`、退出码 0/254/255 这条路需要给控制台进程发 Ctrl+C，Windows 上无法从 pwsh 做到；冒烟进程是 `Stop-Process` 强杀的。同理未验证 `TZ` 非法值拒绝启动。
- 没在 nginx-proxy-manager 后面验证 `X-Forwarded-For`/`X-Forwarded-Proto` 的真实行为（见 U1、U7）。
- 没有真实 mesh 客户端参与，MESH2 相关结论来自代码阅读 + `httptest` 构造的请求，不是端到端联调。

## 5. 复现环境与收尾

- 评审期间新增：本目录（`REVIEW.md` + 6 个探针）。`scripts/` 里的探针是 `package main`，会被 `go vet ./...` 一起编译，已全部 `gofmt -w`，不影响 `just check` 的门禁。
- 结束时 `git status --short`：只有 `?? code-reviews/`。没有 commit、没有 push、没有改动任何被跟踪文件。
- 临时产物（`%TEMP%\mesh-review-smoke`、一次性 `go build` 输出）已删除；本机正在运行的 mesh 进程未被触碰。
