# 协作 v1 安全与信任边界

> 对应任务：`C00-04`（#5），阶段 W0，责任 Agent E。
> 基线：`superdaobo/infinite-canvas@0a5e3b0`（`collab/v1` 集成分支起点）。
> 本文只描述身份、资源授权、学校设备与 NAS 的信任边界与规则，不含任何真实凭据。
> 所有 `file:line` 引用均对照基线真实代码，未在仓库出现的 `ZIP:` 参考 PoC 不作为证据。

## 0. 范围与原则

本文是多人 API（W1 起）开工前的安全前置约定，覆盖：

- 现有 JWT 鉴权模型与协作 v1 的 WS 身份绑定方式。
- 房间 / 成员 / 节点 / 连线 / 媒体 / 学校任务的 owner/editor/viewer 授权矩阵。
- 学校设备执行器（本期 mock）的受限身份边界。
- NAS staging 与生产环境的信任隔离。
- 不得广播或落盘的数据清单。
- 负例清单与预期拒绝行为。

三条硬原则：

1. **默认拒绝**：未明确授权的操作一律拒绝，鉴权失败返回 `401`，越权返回 `403`，不泄漏资源是否存在。
2. **单一身份来源**：房间权限、WS 连接、设备执行都必须归因到同一账号 ID（`model.AuthUser.ID`），不引入第二套身份。
3. **凭据零落盘**：任何 Cookie、令牌、密钥、设备令牌、学校账号都不得进入仓库、Issue、PR、日志或协作广播。

## 1. 身份

### 1.1 现有 JWT 鉴权模型

- 令牌签发：`service/auth.go:434` `newToken`，使用 HS256（`jwt.SigningMethodHS256`），claims 为 `TokenClaims{UserID, Username, Role}` + `RegisteredClaims{ExpiresAt, IssuedAt, Subject=user.ID}`（`service/auth.go:29`、`service/auth.go:439`）。默认有效期 `JWT_EXPIRE_HOURS=168` 小时（`config/config.go:19`）。
- 令牌校验：`service/auth.go:210` `ParseToken` 强制 HMAC 签名方法，`service/auth.go:224` `CurrentAuthUser` 再回库校验用户存在且状态非 `ban`（`service/auth.go:229`、`service/auth.go:233`）。因此**撤销用户 / 封禁账号会即时使旧令牌失效**。
- 中间件三档：
  - `middleware.UserAuth`（`middleware/admin.go:24`）：要求有效令牌且 `Role != guest`，否则 `401`。挂在 `/api/v1` 分组（`router/router.go:40`）。
  - `middleware.OptionalAuth`（`middleware/admin.go:35`）：有令牌则注入用户，无令牌放行；用于 `/api/auth/me`（`router/router.go:23`）、`/api/prompts`（`router/router.go:124`）、`/api/assets`（`router/router.go:129`）。
  - `middleware.AdminAuth`（`middleware/admin.go:13`）：要求 `Role == admin`，挂在 `/api/admin` 分组（`router/router.go:132`）。
- 身份注入载体：`service.WithUser` / `service.UserFromContext`（`service/context.go:11`、`service/context.go:15`），业务层统一从 context 取 `model.AuthUser`，不再信任请求体里的 `userId`。
- 角色枚举：`guest` / `user` / `admin`（`model/user.go:5`）；账号状态 `active` / `ban`（`model/user.go:14`）。
- 匿名访问：`middleware.AnonymousStorage`（`middleware/anonymous_storage.go:25`）为匿名上传签发独立 HS256 Cookie 会话，注入 `Role=guest` 的虚拟身份（`middleware/anonymous_storage.go:39`）。Cookie 名 `infinite_canvas_anonymous_storage`（`middleware/anonymous_storage.go:19`），`Path=/api/anonymous/files`（`middleware/anonymous_storage.go:78`）、`HttpOnly`、`SameSite=Lax`、按需 `Secure`（`middleware/anonymous_storage.go:75`）。**该 Cookie 不会发送到协作或设备接口**，不能当作协作身份。

### 1.2 协作 v1 的 WS 身份绑定

新增 WS 功能文件尚未存在（`grep` 全仓无 WebSocket / `Upgrade(` 代码）。本节为契约，W1 实现时必须满足：

- **建连必须携带与 REST 相同的 JWT**，不得使用独立密钥或独立 token 表。复用 `service.CurrentAuthUser` 校验（`service/auth.go:224`），拿到 `AuthUser.ID` 作为唯一主体。
- **禁止把身份放在 URL query 明文长期传递**：短时 token 只能用于首次握手，且必须一次性、短 TTL、绑定房间；长期会话用 WS 内部心跳续期，不落日志。
- **建连时立即做房间成员解析**：以 `(roomID, userID)` 查成员表，得到 `role` 后再 `join`；无权者直接 `4403` 关闭，不发送房间快照。
- **断线重连**：重连仍走同一 JWT + 成员校验，不得因为持有旧连接 ID 而跳过鉴权。
- **写操作二次鉴权**：WS 收到的每个变更操作都必须按当前成员角色校验（见第 2 节），不得只在建连时校验一次就信任后续帧。
- **成员被移除 / 邀请被撤销 / 账号被封禁**：服务端应主动关闭该用户在该房间的所有 WS 连接（`CurrentAuthUser` 的 ban 检查已提供数据依据）。

## 2. 资源授权矩阵

### 2.1 角色定义

| 角色 | 来源 | 说明 |
|---|---|---|
| 匿名（anonymous） | 无令牌或 `Role=guest` | 仅能访问匿名存储、公开读接口 |
| viewer | 房间成员 role=viewer | 只读 |
| editor | 房间成员 role=editor | 读写画布，不能管成员 |
| owner | 房间创建者 / role=owner | 全权，含成员与删除 |
| device | 设备令牌（非 JWT） | 仅受限任务 API |
| admin | 全局 `Role=admin` | 后台管理，不等于房间 owner |

> 注意：`admin` 是**系统管理员**，不是房间成员角色。管理员可管理系统资源，但访问某个私有房间仍应受房间成员规则约束，避免"管理员默认可读所有私有画布"。

### 2.2 操作矩阵

图例：`Y` 允许，`N` 拒绝，`-` 不适用。

| 操作 | 匿名 | viewer | editor | owner | device | admin |
|---|---|---|---|---|---|---|
| 读取房间快照 / 节点 | N | Y | Y | Y | N | 需为成员或明确授权 |
| 创建 / 编辑 / 移动 / 删除节点 | N | N | Y | Y | N | 需为成员 |
| 创建 / 删除连线 | N | N | Y | Y | N | 需为成员 |
| 上传 / 关联媒体到房间 | N | N | Y | Y | N | 需为成员 |
| 下载 / 播放房间媒体 | N | Y | Y | Y | N | 需为成员 |
| 删除房间级媒体 | N | N | N | Y | N | 需为成员 |
| 生成（调用模型 / 学校任务） | N | N | Y | Y | N | 需为成员 |
| 邀请成员 / 改成员角色 | N | N | N | Y | N | N |
| 撤销邀请 / 移除成员 | N | N | N | Y | N | N |
| 转让 / 删除房间 | N | N | N | Y | N | N |
| 查看成员列表 | N | Y | Y | Y | N | 需为成员 |
| 提交学校受限任务 | N | N | Y | Y | N | N |
| 执行学校受限任务 | N | N | N | N | Y | N |
| 后台用户 / 系统配置 | N | N | N | N | N | Y |

### 2.3 撤销邀请与移除成员后的行为

- **撤销邀请（pending）**：邀请链接/令牌立即失效；持该邀请令牌的请求返回 `403`，不创建成员关系。
- **移除成员（active → removed）**：
  - 该用户对该房间的 REST 请求返回 `403`（不再视为成员）。
  - 主动关闭其该房间 WS 连接，停止推送后续变更。
  - 其已创建的节点/媒体**保留在房间内**（属房间数据），但归因保留原 `createdBy`，便于审计。
  - 若其曾通过旧分享链接访问，链接必须随成员关系失效（分享令牌与成员绑定，见 2.4）。
- **角色降级（editor → viewer）**：下一次写操作即被拒绝（写操作二次鉴权，见 1.2），无需重连。
- **账号封禁（`status=ban`）**：`CurrentAuthUser` 已返回未授权（`service/auth.go:233`），所有令牌即时失效。

### 2.4 分享令牌

- 分享令牌必须：绑定 `roomID`、有明确 TTL、可单独撤销、只授予 viewer 或 editor，不得授予 owner 能力。
- 分享令牌**不是**登录令牌：不能用于 `/api/v1` 其他接口，也不能升级为账号会话。
- 分享令牌泄漏时的止损：撤销该令牌即可，不影响账号 JWT；撤销后所有使用该令牌的 WS 与 REST 请求立即 `403`。

### 2.5 现有代码中的资源归属先例（W1 复用）

协作 v1 的房间/成员授权是新表，但可复用既有"按 ownerID 归属"的写法：

- 画布项目按 `user.ID` 隔离：`service/canvas_project.go:64`、`service/canvas_project.go:103`、`service/canvas_project.go:128`。
- 存储对象删除校验归属：`service/storage.go:300`，`object.CreatedBy != user.ID` 时返回"无权删除该对象"（`service/storage.go:308`）。
- 用户直传对象登记校验归属前缀：`service/storage.go:253`，要求对象键以 `path.Join(provider.PathPrefix, user.ID)/` 开头（`service/storage.go:274`）。
- 设备（Bridge）按 `(OwnerScope, OwnerID)` 隔离：`service/comfy_bridge.go:65`、`service/comfy_bridge.go:355`。

**已知弱点（W1 需修复）**：`service.StorageObjectInfo`（`service/storage.go:146`）不带归属校验，配合公开路由 `GET /api/files/:id`（`router/router.go:26`）与 `GET /api/files/:id/content`（`router/router.go:29`），当前任意人知道对象 ID 即可读元数据与内容。协作 v1 的房间媒体必须改为**按房间成员鉴权**，不得沿用公开对象 ID 直读。

## 3. 学校设备受限身份

### 3.1 信任边界

学校设备执行器（本期 mock，后续为专用 Chrome + Comfy Bridge，见 #19/#20）属于**受限设备身份**，与用户 JWT 完全分离：

- **独立凭据**：设备持有设备令牌，不是用户 JWT。现有先例是 Comfy Bridge：64 位十六进制 token，服务端只存 SHA-256 哈希（`service/comfy_bridge.go:116`、`service/comfy_bridge.go:390`），明文仅在注册时返回一次（`service/comfy_bridge.go:65`）。
- **独立路由组**：设备接口在 `/api/bridge/comfy/*`（`router/router.go:157`），**不挂 `UserAuth`**，由 handler 内部按 `Authorization: Bearer <token>` 认证（`handler/comfy_bridge.go:255`、`canvas-agent/native/comfy-bridge/main.go:689`）。学校设备接口应沿用同一模式并单独分组。
- **归属绑定**：设备必须绑定 `(OwnerScope, OwnerID)`（`model/comfy_bridge.go:7`）。`personal` 归属用户、`system` 归属系统（`handler/comfy_bridge.go:14`、`handler/comfy_bridge.go:17`）。学校设备应绑定为受控的系统级归属，不允许任意用户注册为学校设备。

### 3.2 设备只允许访问受限任务 API

设备身份**只能**访问：

- `POST /api/bridge/.../heartbeat`：上报能力与在线状态（`handler/comfy_bridge.go:104`）。
- `GET  /api/bridge/.../poll`：领取分配给自己的任务（`handler/comfy_bridge.go:123`）。
- `POST /api/bridge/.../lease`：续租（`handler/comfy_bridge.go:186`）。
- `POST /api/bridge/.../result`：回传结果（`handler/comfy_bridge.go:220`）。

设备身份**禁止**访问：

- 任何 `/api/v1/*` 用户接口、`/api/admin/*` 管理接口。
- 房间成员、房间列表、其他用户画布项目。
- 用户模型配置、模型密钥、存储凭据（`/api/v1/user-config`、`/api/v1/user-config/model`，`router/router.go:93`、`router/router.go:94`）。
- 其他设备或用户的 Bridge 列表（`GET /api/v1/comfy-bridges` 按 `user.ID` 过滤，`handler/comfy_bridge.go:21`）。

### 3.3 设备不得列举他人私有画布或密钥

- 设备只能拿到服务端**已分配给它的**任务载荷，不能主动查询任务列表。
- 领取任务必须走租约（lease），且回传时校验 `request.BridgeID/OwnerScope/OwnerID` 一致，否则拒绝（`service/comfy_bridge.go:246`、`service/comfy_bridge.go:292`）。
- 任务载荷中不得包含其他用户身份、其他房间节点、模型密钥或学校账号 Cookie；只放完成该任务所需的最小参数（工作流、参考图 URL、输出目标节点 ID）。
- 参考图等媒体应通过**房间内受限、短时、可撤销**的 URL 下发，不得下发长期公开的对象地址。

### 3.4 学校账号与原任务绑定规则

- 学校账号（Cookie / 登录态）**只存在于设备侧**，由设备本地受控 Chrome Profile 管理，**绝不下发到服务端数据库、协作广播或前端**。
- 一个学校任务必须绑定：提交人 `userID`、房间 `roomID`、幂等键、执行设备 ID。执行结果回填时按此绑定写回，禁止跨任务复用。
- 设备本地配置持久化必须限权：现有 Bridge 以 `0700` 建目录、`0600` 写配置（`canvas-agent/native/comfy-bridge/main.go:183`、`canvas-agent/native/comfy-bridge/main.go:187`），学校设备沿用并更严。
- 幂等与串行：同一幂等键只允许一次真实执行（Epic 要求 FIFO 单执行槽），重复请求必须命中幂等记录返回既有结果，不得重复触发学校账号操作。
- 出网 SSRF：设备回传的产物 URL、服务端代理的媒体 URL 都必须经过 SSRF 白名单校验（见 5.4），防止设备被诱导访问内网。

## 4. NAS / staging 信任边界

### 4.1 生产环境不变

- 协作 v1 **不改变现有生产部署**：生产库、生产 NAS 服务、生产 Cloudflare 配置在 W0–W4 期间不动。
- 任何涉及生产库备份、生产 NAS、Cloudflare token 的操作都需要**单独审批**，不属于本任务范围。

### 4.2 staging 隔离

- 独立 staging 部署在 FDL NAS 上（见 #27），使用**独立数据库、独立数据目录、独立域名/端口**，不复用生产 DSN。
- staging 的 `JWT_SECRET`、管理员口令、存储凭据必须是**独立的一套**，与生产不共用。默认值（`config/config.go:16`、`config/config.go:18`）仅用于本地开发，staging/生产必须覆盖。
- 当 `JWT_SECRET` 为空或仍为默认值时，程序会生成随机密钥（`config/config.go:36`、`config/config.go:41`）——这会导致重启后旧令牌全部失效，staging 应显式设置固定密钥避免误判。
- staging 与生产的媒体存储桶/目录必须隔离，staging 不得写入生产 NAS 路径。

### 4.3 凭据不进仓库

- `.gitignore` 忽略 `.env*`（保留 `.env.example`）、`data/`、`*.log`（`.gitignore`）；`.dockerignore` 同样排除 `.env*`（`.dockerignore`）。
- `.env.example` 只放占位默认值，不放真实凭据。
- 禁止提交：真实 `JWT_SECRET`、管理员口令、数据库 DSN、NAS `master.key`、Cloudflare token、学校 Cookie、Chrome Profile、Bridge 设备令牌。
- 提交前自检：`git diff --cached` 中不得出现上述任何真实值；发现即从暂存区移除并轮换该凭据。

## 5. 不得广播 / 落盘的数据清单

### 5.1 不得进入协作广播（WS / 实时消息）

- 私有 Agent 聊天内容、导演台草稿、个人提示词。
- 模型密钥、用户模型配置（`/api/v1/user-config/model`，`handler/user_data.go:19`）。
- 存储凭据（S3/WebDAV AccessKey、密钥）。
- 学校 Cookie、Chrome Profile 路径、设备令牌。
- 其他用户身份明细（邮箱、算力点、登录时间）。

广播只应包含：房间图数据（节点/连线）、成员公开信息（ID、显示名、角色）、在线状态、任务状态（不含凭据）。

### 5.2 不得落盘 / 落日志

- 明文 JWT、分享令牌、设备令牌、匿名存储 Cookie。
- 学校账号 Cookie、Chrome Profile、CDP 端口凭据。
- 模型 API Key、存储 AccessKey/SecretKey。
- 服务端日志不得打印上述字段；现有日志打印的是 `bridge=%s task=%s err=%v` 等 ID 级信息（`service/comfy_bridge.go:105`），应保持只打 ID 不打凭据。

### 5.3 用户模型配置的既有边界

- 现状：AI API Key 存在浏览器本地并由前端直连 OpenAI 兼容接口（`AGENTS.md` 项目注意事项）。
- 用户模型配置存于 `model.UserConfig.ModelConfig`（`model/user_config.go`），按 `user.ID` 隔离（`service/user_data.go:147`）。
- 协作 v1 不得把该配置读入房间广播或下发给设备；设备任务只带任务所需模型标识，不带密钥。

### 5.4 出网 SSRF 边界（现有实现，协作复用）

- 代理图片入口 `GET /api/proxy-image`（`router/router.go:123`）当前**无鉴权**，依赖 SSRF 防护兜底（`handler/storage.go:185`）。
- SSRF 防护：`service.SafeProxyHTTPClient`（`service/ssrf.go:15`）禁止代理、限制重定向 ≤5 次且仅 http/https（`service/ssrf.go:25`），拨号前解析并拦截本地/内网/链路本地/组播/`100.64.0.0/10`（含云 metadata）地址（`service/ssrf.go:38`、`service/ssrf.go:71`）。
- 协作 v1 新增任何服务端拉取（媒体回填、参考图下载、设备产物回传）都必须走同一 `SafeProxyHTTPClient`，不得使用 `http.DefaultClient` 直连用户可控 URL。

## 6. 负例清单

每条给出预期拒绝行为。均为规则描述，不含真实凭据。

| # | 场景 | 预期行为 |
|---|---|---|
| N1 | 无令牌访问 `/api/v1/*` | `401 未登录或权限不足`（`middleware/admin.go:24`） |
| N2 | 伪造/过期/错误签名的 JWT | `401`；`ParseToken` 拒绝非 HMAC 方法（`service/auth.go:210`） |
| N3 | 已封禁账号持旧令牌访问 | `401`；`CurrentAuthUser` 因 `status=ban` 拒绝（`service/auth.go:233`） |
| N4 | viewer 尝试创建/编辑/删除节点、连线、媒体 | `403`，操作不落库，不广播 |
| N5 | viewer 尝试生成或提交学校任务 | `403` |
| N6 | 非成员访问房间快照 / WS 建连 | `403` 或 WS `4403` 关闭，不发送快照 |
| N7 | 撤销邀请后仍用旧邀请令牌 | `403`，不创建成员关系 |
| N8 | 移除成员后仍用旧分享链接 / 旧 WS 连接 | `403`，旧 WS 被主动关闭 |
| N9 | editor 尝试邀请/移除成员 | `403`（仅 owner 可管成员） |
| N10 | 分享令牌尝试访问 `/api/v1` 其他接口 | `403`（分享令牌非登录令牌） |
| N11 | 设备令牌调用 `/api/v1/*` 或 `/api/admin/*` | `401/403`，设备身份不适用于用户接口 |
| N12 | 设备尝试列举他人画布 / Bridge 列表 / 密钥 | `403`；列表接口按 `user.ID` 过滤（`handler/comfy_bridge.go:21`） |
| N13 | 设备领取不属于自己的任务 / 回传他人任务结果 | `409/403`；`OwnerScope/OwnerID` 校验失败（`service/comfy_bridge.go:246`） |
| N14 | 无效/被禁用的设备令牌 | `401 Bridge Token 无效`（`handler/comfy_bridge.go:255`） |
| N15 | 服务端代理拉取 `127.0.0.1` / `169.254.169.254` / 内网地址 | 拒绝："禁止访问本地或内网地址"（`service/ssrf.go:49`、`service/ssrf.go:71`） |
| N16 | 开放重定向（`//evil.com`、`/\evil.com`、`javascript:`） | 归一为 `/`（`service/auth.go:568`；测试 `service/auth_redirect_test.go:8`） |
| N17 | 匿名身份尝试访问房间 / 协作接口 | `403`；匿名 Cookie 仅作用于 `/api/anonymous/files`（`middleware/anonymous_storage.go:78`） |
| N18 | 用对象 ID 读取他人房间媒体 | 协作 v1 必须 `403`（当前 `service/storage.go:146` 无归属校验，属待修复弱点，见 2.5） |

## 7. Cloudflare Access 叠加与应急撤销

### 7.1 与房间权限的叠加规则

- Cloudflare Access 是**边缘层**控制，决定"能不能到达服务"；房间权限是**应用层**控制，决定"到了能做什么"。两者是**与**关系：Access 放行但非房间成员 → 应用层 `403`；Access 拦截 → 请求根本到不了应用。
- 协作 v1 的 staging 建议对 `/api/*` 与 WS 路径启用 Access 作为额外防线，但**不得**把 Access 当作房间权限的替代。
- Access 身份与 JWT 账号不自动等同：即使 Access 放行，WS/REST 仍必须完成 JWT + 成员校验（见 1.2）。

### 7.2 应急撤销路径（按影响面从小到大）

1. **撤销单个分享令牌 / 邀请**：立即生效，不影响其他成员。
2. **移除单个成员**：关闭其 WS 连接，后续请求 `403`。
3. **降级角色**：editor → viewer，写操作立即被拒。
4. **封禁账号**：`status=ban`，所有 JWT 即时失效（`service/auth.go:233`）。
5. **轮换 `JWT_SECRET`**：使全部令牌失效（全员需重新登录），用于大规模泄漏。注意默认随机密钥重启也会导致此效果（`config/config.go:36`），生产必须显式管理。
6. **禁用设备令牌**：将 Bridge `Enabled=false`，认证即失败（`service/comfy_bridge.go:121`）。
7. **边缘层封禁**：在 Cloudflare Access/WAF 拦截来源 IP 或路径，作为最后手段。
8. **staging 隔离降级**：直接停掉 staging 环境，不影响生产。

## 8. 对旧单人模式与现有生产的影响

- 本文为文档，不改任何业务代码、不改 `AGENTS.md`、不触碰热点文件。
- 现有单人模式（本地画布 + 账号同步）不受影响；`/api/v1` 鉴权模型不变。
- 现有生产部署不变；所有协作 v1 变更先在 NAS staging 验证。
- 唯一需要在 W1 修复的既有弱点：`GET /api/files/:id` 与 `/content` 的公开可读（见 2.5、N18），协作房间媒体必须叠加成员鉴权。

## 9. 未解决风险与待决项

- **R1**：`/api/proxy-image` 与 `/api/files/:id/content` 当前匿名可用，依赖 ID 不可猜测性；协作媒体需改为成员鉴权（待 W1/#20 决策）。
- **R2**：WS 身份绑定为契约设计，实现文件尚不存在，需在 #10 落地并补充测试。
- **R3**：分享令牌的存储与撤销机制（是否单表、是否轮换）待 #7 决策。
- **R4**：学校设备与"系统级归属"的具体绑定方式（专用 ownerScope 值）待 #17/#19 决策。
- **R5**：Cloudflare Access 与 JWT 账号的映射策略（是否需要 Access JWT 校验）待部署任务 #27 决策。
- **R6**：staging 密钥与生产密钥的轮换流程需在 #27/#30 形成运维手册。
