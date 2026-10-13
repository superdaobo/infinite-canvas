# 协作 v1 多人权限、会话、请求与媒体安全审计（C05-02 / #28）

> 任务：`C05-02`（W5，Agent E）。审计基线：`superdaobo/infinite-canvas@a537d72`（`feat/c05-02-security-audit`）。
> 对照文档：`docs/collab-v1/security-boundaries.md`（授权矩阵与负例 N1–N18）、`docs/collab-v1/contract.md`。
> 本文所有 `file:line` 均对照本分支真实代码；结论分「已实现 / 部分实现 / 未实现 / 未验证」，未真实运行验证的一律标注「未验证」，不把「代码看起来对」写成「已验证」。

## 0. 结论摘要

| 维度 | 结论 |
|---|---|
| 多人权限（房间/成员/节点/连线/媒体/学校任务） | **已实现**，授权集中在 service 层（`RequireRoomWriter/Owner`）与写事务内（`ApplyRoomBatch` 复核 `CanWrite`），非成员统一 404/4403 不泄漏存在性 |
| 会话：JWT 生命周期与撤销 | **已实现**；封禁/删除账号即时失效 JWT（回库校验），本次补充**封禁/删除即时断开 WS** 与**建连时封禁校验** |
| 会话：WS ticket 生命周期 | **已实现**；一次性、30s TTL、绑定 `(userId,roomId)`，消费即删（`TestTicketSingleUseAndExpiry`） |
| 请求：请求体上限 | **部分实现**；协作操作/媒体/WS/匿名路径有上限，遗留 `POST /api/v1/files` 无上限（见 §6.1，需决策） |
| 请求：越权列举 | **已实现**；列表接口按身份过滤，非成员 404 |
| 请求：SSRF | **已实现**；出网统一走 `SafeProxyHTTPClient`，拦截回环/内网/链路本地/`100.64/10`（`TestIsBlockedProxyIP`） |
| 请求：CORS / Origin / CSRF | **部分实现（有意取舍）**；无 Cookie 认证故 CSRF 不适用；WS 关闭 Origin 校验（依赖 ticket 绑定，见 §6.4） |
| 请求：反向代理限流 | **未实现**（应用层无任何限流；需在 Cloudflare/nginx 层落地，见 §6.5） |
| 媒体：房间媒体授权/nosniff/主动内容/Range | **已实现**，本次补充修复**公开文件路径与图片代理的主动内容 XSS** |
| 负例 N1–N18 | 16 项**已实现**（有测试或可验证路径），N10 以「邀请令牌非登录令牌」满足，2 项（N3 的 WS 侧、N18 的通用对象侧）**部分实现**并已修复可修部分 |

本次共修复 **3 组高危/中高危问题**（§7），新增 5 个负例测试；`go build ./...`、`go test ./...` 全绿（原始输出见 PR 正文）。

---

## 1. 身份与会话

### 1.1 JWT

- 签发：HS256，`TokenClaims{userId,username,role}` + `RegisteredClaims`（`service/auth.go:29`、`service/auth.go:444` `newToken`）。
- 校验：`service/auth.go:211` `ParseToken` 强制 HMAC 签名方法，拒绝非 HMAC；`service/auth.go:225` `CurrentAuthUser` 回库校验用户存在且状态非 `ban`（`service/auth.go:234`）。
- 结论：**已实现**。撤销/封禁账号会使旧 JWT 立即失效（回库校验）；角色变更同样即时生效（每次请求取库中角色）。
- 中间件：`middleware/admin.go:13` `AdminAuth`、`:24` `UserAuth`（拒绝 `guest`）、`:35` `OptionalAuth`。`/api/v1` 组挂 `UserAuth`（`router/router.go:40`）。

### 1.2 WS 身份绑定与 ticket

- 建连端点 `GET /api/v1/collab/rooms/:id/ws` **不挂 `UserAuth`**（`router/router.go:185`），身份来自首帧 `hello` 的 ticket：`internal/collab/realtime/conn.go:63` `DefaultTickets.Consume`，校验 ticket 绑定的 `roomID` 与连接房间一致。
- ticket：32 字节随机、`base64url`；TTL 30s（`internal/collab/realtime/ticket.go:11`）；`Consume` 原子删除实现单次使用（`ticket.go:59`），过期/已用/不存在统一 `false`。
- 建连后**二次成员解析**：`conn.go:69` → `service/room_realtime.go:57` `ResolveMember` 按成员表查角色，非成员 `4403`，不发快照。
- 写操作**不信任 WS 帧**：`conn.go:198` `readLoop` 只处理 `cursors`，未知/重复 `hello` 一律忽略，节点/连线写入只能走 REST `/operations` 的成员二次鉴权。
- 结论：**已实现**。

### 1.3 撤销后的即时失权（会话侧）

| 事件 | REST | WS | 证据 |
|---|---|---|---|
| 移除成员 | 立即 404/403 | 主动关闭 4401 | `service/room_invite.go:223` `DisconnectUser`；测试 `TestWSDisconnectOnMemberRemoval` |
| 角色降级 editor→viewer | 下次写即 403 | 主动关闭（强制按新角色重连） | `service/room_invite.go:259-261`；测试 `TestCollabAccessContractSQLite` |
| 撤销邀请 | 旧令牌 403，不创建成员 | 不适用 | `service/room_invite.go:130-136`；测试 `TestCollabAccessContractSQLite` |
| 房间删除 | 404 | 主动关闭 4401 | `service/room.go:127` `DisconnectRoom` |
| **账号封禁** | 401（`CurrentAuthUser` 回库） | **本次修复**：主动关闭 4401 + 建连拒绝 4403 | 修复前 `DisconnectUserEverywhere` **无任何调用点**（§7.3） |

> 修复前缺口：`DisconnectUserEverywhere`（`internal/collab/realtime/hub.go:181`）在非测试代码中**从未被调用**，封禁账号已建立的 WS 连接不会被关闭，且封禁前签发的未使用 ticket 仍可建连（TTL 30s 窗口）。属 N3 的会话侧部分实现。

---

## 2. 授权矩阵（对照 security-boundaries.md §2.2）

图例：`Y` 允许，`N` 拒绝，`-` 不适用。结论列给 `file:line` 与验证方式。

| 操作 | 匿名 | viewer | editor | owner | device | admin | 实现位置 / 验证 |
|---|---|---|---|---|---|---|---|
| 读取房间快照/节点 | N | Y | Y | Y | N | 需为成员 | `service/room.go:79` `CurrentUserRoom`；`repository/room_graph.go:127` `ListRoomGraph`；`service/room_op.go:56` `ReadRoomGraph` 校验成员。`TestCollabRoomContractSQLite` |
| 创建/编辑/移动/删除节点 | N | N | Y | Y | N | 需为成员 | 写事务内 `repository/room_graph.go:34-42` 复核 `CanWrite`。`TestWSViewerCanConnectButCannotWrite`、`TestCollabRoomOperationsAtomicAndIdempotent` |
| 创建/删除连线 | N | N | Y | Y | N | 需为成员 | 同上（`applyOperations` 内 `connectEdge/disconnectEdge`） |
| 上传/关联媒体到房间 | N | N | Y | Y | N | 需为成员 | `service/room_media.go:66` `UploadRoomMedia` → `RequireRoomWriter`。`TestCollabRoomMediaContract`（viewer 403 / 非成员 404 / 匿名 401） |
| 下载/播放房间媒体 | N | Y | Y | Y | N | 需为成员 | `service/room_media.go:193` `RoomMediaContent` → `CurrentUserRoomMember` |
| 删除房间级媒体 | N | N | N | Y | N | 需为成员 | `service/room_media.go:212` `DeleteRoomMedia` → `RequireRoomOwner`。`TestCollabRoomMediaContract`（viewer 403） |
| 生成（模型/学校任务） | N | N | Y | Y | N | 需为成员 | `service/school_queue.go:43-50` `SubmitSchoolTask` → `RequireSchoolTaskSubmitter`。`TestRequireSchoolTaskSubmitter` |
| 邀请成员/改角色 | N | N | N | Y | N | N | `service/room_invite.go:49/229` → `RequireRoomOwner`。`TestCollabAccessContractSQLite` |
| 撤销邀请/移除成员 | N | N | N | Y | N | N | `service/room_invite.go:86/193`。`TestCollabAccessContractSQLite` |
| 转让/删除房间 | N | N | N | Y | N | N | `service/room.go:104` `DeleteCurrentUserRoom`（仅 owner；**转让接口未实现**） |
| 查看成员列表 | N | Y | Y | Y | N | 需为成员 | `service/room_invite.go:159` `RoomMembers` |
| 提交学校受限任务 | N | N | Y | Y | N | N | 同「生成」 |
| 执行学校受限任务 | N | N | N | N | Y | N | `/api/bridge/school/*`（`router/router.go:228`）Bearer 设备令牌，独立于用户 JWT |
| 后台用户/系统配置 | N | N | N | N | N | Y | `router/router.go:197` `AdminAuth` |

**admin ≠ 房间 owner**：`admin` 仅能进 `/api/admin/*`；访问私有房间仍需房间成员关系（`CurrentUserRoomMember` 不因 `Role=admin` 放行）。**已实现**，符合 security-boundaries §2.1 注释。

---

## 3. 负例清单 N1–N18 逐项结论

| # | 场景 | 结论 | 证据 / 测试 |
|---|---|---|---|
| N1 | 无令牌访问 `/api/v1/*` | 已实现 | `middleware/admin.go:24-33`；`TestCollabAndCanvasProjectsRequireAuth`、`TestCollabAccessRoutesRequireAuth` |
| N2 | 伪造/过期/错误签名 JWT | 已实现 | `service/auth.go:211-222`（强制 HMAC） |
| N3 | 已封禁账号持旧令牌 | **部分实现→本次补全** | REST 侧 `service/auth.go:225-236`；WS 侧修复见 §7.3。`TestWSDisconnectOnAccountBan`、`TestWSConnectRejectedForBannedAccount` |
| N4 | viewer 建/改/删节点、连线、媒体 | 已实现 | `repository/room_graph.go:40`；`service/room_media.go:66`。`TestWSViewerCanConnectButCannotWrite`、`TestCollabRoomMediaContract` |
| N5 | viewer 生成/提交学校任务 | 已实现 | `service/school_task.go:75`、`service/school_queue.go:48`。`TestRequireSchoolTaskSubmitter` |
| N6 | 非成员访问快照 / WS 建连 | 已实现 | REST `service/room_op.go:62-66`（404）；WS `conn.go:74-77`（4403）。`TestWSNonMemberRejected` |
| N7 | 撤销邀请后旧令牌 | 已实现 | `service/room_invite.go:130-136`。`TestCollabAccessContractSQLite`（撤销后 `ErrInviteInvalid`、不建成员） |
| N8 | 移除成员后旧链接/旧 WS | 已实现 | REST 404；WS 4401（`room_invite.go:223`）。`TestWSDisconnectOnMemberRemoval` |
| N9 | editor 邀请/移除成员 | 已实现 | `RequireRoomOwner`。`TestCollabAccessContractSQLite` |
| N10 | 分享令牌访问 `/api/v1` 其他接口 | 已实现（以邀请令牌语义满足） | 系统**未实现独立的「分享令牌」**（`security-boundaries.md` R3）；邀请令牌仅经 `POST /api/v1/collab/invites/accept`（需登录 JWT）消费，不是登录凭据，无法用于其他 `/api/v1` 接口。`router/router.go:139` |
| N11 | 设备令牌调用 `/api/v1/*` 或 `/api/admin/*` | 已实现 | 设备令牌非 JWT，`UserAuth`/`AdminAuth` 均拒。`TestCollabSchoolRoutesRequireAuth`、`TestSchoolDeviceExecutorEndToEnd` |
| N12 | 设备列举他人画布/Bridge/密钥 | 已实现 | `service/school_device.go:203` `ListExecutorSchoolTasks` 按 `executorID` 过滤。`TestListExecutorSchoolTasksScoped` |
| N13 | 设备领取/回传他人任务 | 已实现 | `service/school_queue.go:318-334` `requireRunningSchoolTask` 校验 `ExecutorID`。`TestUploadSchoolTaskResultRejectsWrongExecutor`、`TestSchoolAccountBindingBlocksResubmit` |
| N14 | 无效/被吊销设备令牌 | 已实现 | `service/school_device.go:223-245`（`Enabled=false` 即拒）；`service/school_executor_auth.go:46`。`TestSchoolDeviceRegisterRotateRevokeAuth`、`TestSchoolDeviceStaticTokenFallback` |
| N15 | 代理拉取 `127.0.0.1`/`169.254.169.254`/内网 | 已实现 | `service/ssrf.go:38-91`；`TestIsBlockedProxyIP`、`TestSafeProxyHTTPClientRedirectLimit` |
| N16 | 开放重定向（`//evil.com`、`/\evil.com`、`javascript:`） | 已实现 | `service/auth.go:578-589` `safeRedirectPath`；`TestDecodeStateRejectsOpenRedirect`、`TestSafeRedirectPath` |
| N17 | 匿名身份访问房间/协作接口 | 已实现 | 匿名 Cookie 仅 `Path=/api/anonymous/files`（`middleware/anonymous_storage.go:78`），协作接口在 `/api/v1`（`UserAuth`）。`TestCollabAccessRoutesRequireAuth` |
| N18 | 用对象 ID 读他人房间媒体 | **部分实现→本次收窄** | 房间媒体 ID 经公开路径一律 404（`service/room_media.go:238` `AuthorizeFileRead`，`handler/storage.go:140/177`）。`TestCollabRoomMediaContract`。**残留**：非房间媒体（单人/匿名对象）仍按 ID 公开可读，见 §8 R1 |

---

## 4. 多人权限越权面（补充审计）

- **节点/连线写入**：service 层未显式调 `RequireRoomWriter`，但写事务内 `repository/room_graph.go:34-42` 复核成员角色并在非可写时 `forbidden`/`not_found`，等价防护且更贴近数据（并发降级也生效）。**已实现**。
- **撤销/重做**：按 actor 隔离 + 仍须可写成员（`repository/room_history.go:225-237`）。**已实现**。`TestCollabRoomHistoryUndoIsolatedPerUser`、`TestCollabRoomHistoryPermissionAndNotFound`。
- **学校结果回填**：`ApplyRoomBatchAsServer`（`repository/room_graph_server.go:28`）刻意跳过成员校验，但**仅服务端内部调用**，且授权前置为「执行设备持有本房间 running 任务」（`service/school_result.go:88-98`）。提交人被移除/降级后仍能回填并释放执行槽（`TestSchoolResultBackfillAfterSubmitterRemoved/Downgraded`）。**已实现**，调用面已核查（仅 `service/school_backfill.go:27` 使用，无 HTTP 入口）。
- **媒体 ID 不可猜测性不作为授权**：媒体授权走成员关系，`storageKey` 即便公开也不直接可读（`docs/collab-v1/contract.md` §8.2 开放点 3 已说明）。**已实现**。

---

## 5. 会话生命周期（补充）

- **WS ticket 复用/重放**：`TicketStore.Consume` 命中即删（`ticket.go:59-71`）。`TestTicketSingleUseAndExpiry`、`TestServeHandshakeAndSnapshot`。**已实现**。
- **协议版本不匹配**：`conn.go:58` `validateHello` → 4401。`TestProtocolMismatchCloses4401`。**已实现**。
- **连接数上限**：`conn.go:78` `MaxConnectionsPerRoom=64` → 4429。**已实现**（未单独测试，标注**未验证**）。
- **心跳掉线**：`conn.go:227` `pingLoop` 30s。**已实现**（未单独测试）。
- **残留**：ticket 为**进程内**存储（`ticket.go:83`），多实例部署需共享存储（文档已注明）。**未验证**（单实例）。

---

## 6. 请求面

### 6.1 请求体上限
- 协作操作：4 MiB（`handler/collab_room_op.go:17`，解码前 `MaxBytesReader`），超限 413。`TestCollabApplyOperations_OversizedBodyReturns413`。
- 房间媒体上传：`MaxRoomMediaBytes+1MiB`（`handler/collab_media.go:16`）；服务端落盘上限 64 MiB（`service/room_media.go:44`）。`TestCollabRoomMediaContract`（413）。
- WS 单帧：1 MiB（`internal/collab/realtime/conn.go:24/52`）。
- 匿名上传：129 MiB（`middleware/anonymous_storage.go:22`）。
- **缺口（未实现）**：`POST /api/v1/files`（`handler/storage.go:60`）与 `UploadFile` 用 `io.ReadAll` 无上限（`handler/storage.go:67`），认证用户可造成内存放大。**未修复**，见 §8 R2（涉及既有单人上传行为，需产品确认上限值）。

### 6.2 越权列举
- 房间列表按成员关系（`service/room.go:61` `ListRoomsByUser`）；成员列表、媒体列表、学校队列均需成员身份；非成员统一 404（不泄漏存在性）。**已实现**。

### 6.3 SSRF
- 出网统一走 `service.SafeProxyHTTPClient()`（`service/ssrf.go:15`）：禁代理、重定向 ≤5 且仅 http/https、拨号前解析并拦截回环/私网/链路本地/组播/未指定/`100.64.0.0/10`（含云 metadata）。
- 使用点已核查：`handler/storage.go:250`（proxy-image）、`service/school_result.go:365`（学校结果拉取）、`service/storage.go`（S3/WebDAV 出网）。**无 `http.DefaultClient` 直连用户可控 URL**（`service/auth.go` 的 Linux.do 调用为固定受信域名）。**已实现**。

### 6.4 CORS / Origin / CSRF
- `/api/v1` 认证用 `Authorization` 头，**无 Cookie**，故 CSRF 不适用；匿名存储 Cookie `HttpOnly`、`SameSite=Lax`、`Path=/api/anonymous/files`（`middleware/anonymous_storage.go:75-83`），不参与协作鉴权。
- 无全局 CORS 中间件（`router/router.go:11` `gin.Default()`），浏览器同源策略即默认拒绝跨站读。**已实现（默认拒绝）**。
- **WS Origin 校验关闭**：`conn.go:45` `InsecureSkipVerify: true`。取舍理由：身份来自显式 ticket（非 Cookie），跨站页面无法在无 JWT 的情况下换取 ticket，故 CSWSH 不成立。**有意取舍，未验证浏览器跨站场景**；如需纵深防御可加 Origin 白名单（需新增配置，见 §8 R4）。

### 6.5 反向代理限流
- 应用层**无任何限流**（`Register`/`Login`/`/api/bridge/school/*` 等均无限流）。**未实现**。属部署层职责（Cloudflare/nginx），需在 #27/#30 落地。见 §8 R3。

---

## 7. 本次修复

### 7.1 F1（高危）：`GET /api/proxy-image` 反射主动内容 → 同源 XSS
- 问题：代理回显上游 `Content-Type`，`text/html`/`image/svg+xml` 被原样内联返回，且无 `nosniff`。攻击者诱导受害者访问 `https://<app>/api/proxy-image?url=https://evil/x.html` 即可在应用源执行脚本（该端点匿名可用，`router/router.go:188`）。
- 修复：`handler/storage.go:240` `ProxyImage` 改用 `downloadResponseContentType`（`handler/storage.go:201`）：空类型与主动内容（`service.ActiveContentMimeType`，`service/room_media.go:334`，复用 `activeContentMimeTypes` `service/room_media.go:309`）一律降级为 `application/octet-stream` + `Content-Disposition: attachment`，并始终加 `X-Content-Type-Options: nosniff`。
- 测试：`handler/storage_test.go::TestProxyImageNeutralizesActiveUpstreamContent`（`text/html`、`image/svg+xml` 降级；`image/png` 保持内联）、`TestDownloadResponseContentTypeNeutralizesActiveContent`。

### 7.2 F2（高危）：`GET /api/files/:id/content` 存储型 XSS
- 问题：`handler/storage.go:138` `FileContent` 直接回显存储对象声明的 MIME，无 `nosniff`。匿名/认证用户可上传 `Content-Type: text/html` 的对象，再诱导他人访问公开路径，在应用源执行脚本。
- 修复：`FileContent` 加 `nosniff`，主动内容降级为 `octet-stream` + 附件（`handler/storage.go:152-156`）。
- 测试：`handler/storage_test.go::TestFileContentNeutralizesActiveStoredContent`。
- 影响：仅对主动内容改变行为；图片/视频/音频/PDF 等仍内联，单人模式正常素材展示不受影响。

### 7.3 F3（中高危）：账号封禁/删除后 WS 未即时断开
- 问题：`DisconnectUserEverywhere` 无调用点；封禁账号已建立的 WS 继续接收房间广播，封禁前签发的 ticket 在 30s 内仍可建连。
- 修复：
  - `service/auth.go:308-311`：`SaveUser` 保存后若 `status=ban` → `DisconnectUserEverywhere`；
  - `service/auth.go:423-429`：`DeleteUser` → `DisconnectUserEverywhere`；
  - `service/room_realtime.go:57-70`：`ResolveMember` 增加封禁校验，封禁账号建连即 `4403`。
- 测试：`router/collab_ws_test.go::TestWSDisconnectOnAccountBan`（4401）、`TestWSConnectRejectedForBannedAccount`（4403）。

---

## 8. 未修复 / 需决策 / 未验证

- **R1（需架构决策）**：`GET /api/files/:id` 与 `/content`（`router/router.go:26/29`）对**非房间媒体**仍按对象 ID 匿名可读（`service/storage.go:146` `StorageObjectInfo` 无归属校验）。房间媒体已收窄（N18），但通用对象读取仍依赖 ID 不可猜测。彻底修复需签名 URL/短时令牌（涉及 #20/#27 决策）。**未修复**。
- **R2（需产品确认）**：`POST /api/v1/files` 无请求体上限（§6.1）。未修复，避免无依据地改变既有单人上传上限。
- **R3（需部署落地）**：应用层无登录/注册/设备接口限流（§6.5），需在 #27/#30 于 Cloudflare/nginx 层实现。
- **R4（纵深防御，需配置）**：WS Origin 未校验（§6.4，有意取舍）；如需可加 Origin 白名单配置项。
- **R5（残留）**：学校设备管理员 HTTP 接口未实现（`security-boundaries.md` R4），当前仅受控 service 调用（`service/school_device.go:150`）。
- **R6（未验证）**：WS 连接数上限（4429）、心跳掉线、单实例 ticket 存储在真实多实例/长连接场景下的行为未做真实压测；仅代码路径审阅。
- **R7（未验证）**：学校真实设备/学校账号/Chrome Profile 相关边界（#19 本期为 mock）未涉及真实凭据，未验证。
- **未验证项统一说明**：凡未在 `go test` 中断言的行为，本报告均标注「未验证」，不视为通过。

---

## 9. 对旧单人模式与现有生产的影响

- 本次修复只涉及响应头与主动内容降级（F1/F2）及封禁/删除断连（F3），**不改数据库结构、不改路由、不改前端**。
- 单人模式：图片/视频/音频/PDF 仍内联展示；仅主动内容（SVG/HTML/XML/JS）改为附件下载，属安全加固。
- 现有生产部署不变；所有变更先在 NAS staging 验证（#27）。
- 热点文件 `router/router.go`、`repository/db.go` **未改动**。

## 10. 验证方式

```
gofmt（工作区为 CRLF 检出，见 PR 说明）
go build ./...
go test ./...
go test ./handler/ -run 'TestDownloadResponseContentType|TestProxyImage|TestFileContent' -v
go test ./router/  -run 'TestWSDisconnectOnAccountBan|TestWSConnectRejectedForBannedAccount' -v
```

原始输出见 PR 正文。
