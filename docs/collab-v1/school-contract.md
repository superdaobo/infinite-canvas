# 学校共享生成任务契约（C03-01 / W1 定义层）

> 对应任务：`C03-01`（#17），阶段 W1，责任 Agent D。前置：#3（契约）、#5（安全边界）、#6（房间）。
> 基线：`collab/v1` HEAD `ef41063`。
> 规范代码：`model/school_task.go`（领域模型 + 纯校验）、`service/school_task.go`（业务层 + mock 上游契约）。
> 测试：`model/school_task_test.go`、`service/school_task_test.go`。
>
> **本文件只定义契约层**：提交人、执行人、任务幂等、渠道范围、状态机与 mock 上游接口。
> **不实现**持久化与 FIFO 单执行槽（#18）、设备执行器（#19）、H3 参数兼容细节（#21）、媒体回填（#22）。
> 本期学校部分**只做 mock，绝不接触真实设备 / Cookie / Chrome / Comfy Bridge / 学校凭据**。

---

## 0. 范围与不变量

- 学校共享生成是**共享房间内**的多人能力：房间成员提交任务 → 云端按房间串行执行 → 结果回填共享节点。
- 三条硬不变量：
  1. **提交人与执行人分离**：任务同时记录真实用户账号 `submittedBy` 与受限设备身份 `executorId`，两者身份体系完全独立（用户 JWT vs 设备令牌，见 `docs/collab-v1/security-boundaries.md` §3）。
  2. **幂等**：`clientTaskId` 在 `(roomId, submittedBy)` 维度唯一；重复提交返回同一任务，绝不触发第二次学校执行。
  3. **渠道隔离**：学校渠道（千问 / H3）只约束自身参数，**不影响** DeepSeek 等个人模型渠道，也不与之共用队列。
- 本契约只新增独立文件，**不修改**热点文件 `router/router.go`、`repository/db.go`，也不修改已冻结的 `internal/collab/contract/contract.go`。`SchoolTask` 暂不带 gorm 标签，持久化表由 #18 落地。

---

## 1. 提交人（`submittedBy`）

- 提交人是**真实用户账号**：当前登录的 `model.AuthUser.ID`（由 `service.UserFromContext` 从 context 取，见 `service/context.go`、`service/room_op.go:18`）。**不信任请求体里的 `userId`**。
- 提交前提：提交人是该房间的 **owner 或 editor**（`model.RoomMemberRole.CanWrite()`）；viewer 提交返回 `forbidden`（403），对应安全矩阵「提交学校受限任务：viewer N」（`docs/collab-v1/security-boundaries.md` §2.2）。统一入口：`service.RequireSchoolTaskSubmitter`。
- 任务记录字段 `submittedBy` 保存真实账号 ID；回填媒体、审计与「个人取消」（#23）都归因到该账号。
- 提交人**不因任务进入队列而改变**；设备执行结果回填时按 `submittedBy` 归属，禁止跨任务复用（安全边界 §3.4）。

## 2. 执行人（`executorId`，受限设备身份）

- 执行人是**受限设备身份**，与用户 JWT 完全分离（安全边界 §3.1）。设备持有独立设备令牌（参照 Comfy Bridge 的 64 位十六进制 token，服务端只存 SHA-256，`service/comfy_bridge.go`），**不是**用户 JWT。
- 任务字段 `executorId` 在设备领取（lease）任务时绑定；未领取时为空。
- 设备能力边界（安全边界 §3.2/§3.3，本期为契约，由 #19 落地）：
  - 设备**只能**访问受限任务 API（心跳 / 领取 / 续租 / 回传），**禁止**访问 `/api/v1/*`、`/api/admin/*`、房间列表、成员列表、他人画布、模型密钥。
  - 设备只能拿到服务端**已分配给它**的最小任务载荷（工作流、参考图短时 URL、输出目标节点 ID），**不得列举他人画布或密钥**。
  - 设备归属绑定 `(OwnerScope, OwnerID)`，学校设备应为受控的系统级归属（安全边界 R4，具体取值待 #19 决策）。
- 执行器与 mock 学校服务之间的接口见第 6 节。

## 3. 任务幂等

### 3.1 幂等键

- `clientTaskId` 由**提交端生成**，在 `(roomId, submittedBy)` 维度唯一。
- 派生：`model.SchoolTaskIdempotencyKey(roomId, submittedBy, clientTaskId)` = `sha256("school-task-v1\0roomId\0submittedBy\0clientTaskId")` 的十六进制（64 字符），**稳定、可重放、无随机**。
- 任务 ID：`model.SchoolTaskID(...)` = `"school_" + 幂等键前 32 字符`，同样确定性。同一 `clientTaskId` 重试恒得同一任务 ID。

### 3.2 重复提交规则

| 情况 | 行为 |
|---|---|
| 幂等键不存在 | 创建任务（`queued`），派生任务 ID |
| 幂等键已存在，且渠道/参数一致 | **返回既有任务**，`idempotent=true`，**不产生第二次学校执行** |
| 幂等键已存在，但渠道/参数不同 | 拒绝，`operation_id_reused`（409） |

统一入口：`service.ResolveSchoolTaskIdempotency`（纯函数，供 #18 接持久化层调用）。

### 3.3 `upstreamTaskId` 绑定

- `upstreamTaskId` 是 mock/上游学校服务返回的任务号。
- **一旦绑定不可替换**：首次绑定写入；重复绑定同一号幂等成功；绑定到**不同**号返回 `version_conflict`（409）。统一入口：`service.BindSchoolTaskUpstream`。
- `upstreamTaskId` 属于**内部字段**，不进入对外公开视图（见第 7 节）。

## 4. 渠道范围与参数校验

学校渠道与个人模型渠道（DeepSeek 等）**完全隔离**：学校任务按房间使用独立队列作用域（`model.SchoolQueueScope(roomId) = "school:" + roomId`），**不与其他渠道共用队列、不互相串行**（安全边界 §3.4 的串行要求只作用于学校渠道内部）。

| 渠道 | 常量 | 能力 | 参数约束 |
|---|---|---|---|
| 学校千问 | `model.SchoolChannelQwen = "qwen"` | **仅文生图** | `referenceImages` 必须为空；`videoSeconds` 必须为 0 |
| 学校 H3 | `model.SchoolChannelH3 = "h3"` | 有序参考图生视频 | `referenceImages` 数量 **1–9**（复用契约 `contract.ValidateH3ReferenceCount`）；`videoSeconds` 必须是 **4–15** 的整数 |

- H3 参考图为**有序数组**，顺序即语义：第 `i` 张 → 工作流字段 `ref_image_i`（`i=0..8`），对应契约 §6 与 `contract.H3ReferenceField/H3ReferenceMapping`。提交端不得重排。
- 校验入口：`model.ValidateSchoolChannelParams(channel, params)`，返回 `*contract.Error`；越界一律 `invalid_payload`（400）。
- **只约束学校渠道**：上述校验不得施加于其他模型渠道；其他渠道沿用各自既有映射（`service/workflow_mapping.go`）。

## 5. 任务状态机

状态：`queued` / `running` / `succeeded` / `failed` / `cancelled`。

```
queued ──► running ──► succeeded
   │           │
   ├──► failed ├──► failed
   └──► cancelled └──► cancelled
```

合法迁移（`model.CanTransitionSchoolTask` / `model.AllowedSchoolTaskTransitions`）：

| from | 允许的 to |
|---|---|
| `queued` | `running`、`failed`、`cancelled` |
| `running` | `succeeded`、`failed`、`cancelled` |
| `succeeded` / `failed` / `cancelled` | 无（终态） |

- 非法迁移（如 `queued → succeeded`、终态再迁移）返回 `version_conflict`（409），由 `service.ApplySchoolTaskTransition` 统一校验。
- 状态枚举 `model.SchoolTaskStatus`；`Terminal()` 判断终态。

## 6. mock 上游契约（执行器 ↔ mock 学校服务）

本期为**接口定义**，供 #18/#22 用假数据实现，不接触真实设备或凭据。

```go
// service.SchoolUpstreamClient（执行器侧调用）
type SchoolUpstreamTask struct {
    UpstreamTaskID string                 `json:"upstreamTaskId"`
    Status         model.SchoolTaskStatus `json:"status"`
    Progress       int                    `json:"progress"`
    ResultURLs     []string               `json:"resultUrls,omitempty"`
    Error          string                 `json:"error,omitempty"`
}

type SchoolUpstreamClient interface {
    // 提交一个已校验的学校任务，返回上游任务号与初始状态。
    Submit(ctx context.Context, task model.SchoolTask) (SchoolUpstreamTask, error)
    // 查询上游任务当前状态。
    Query(ctx context.Context, upstreamTaskID string) (SchoolUpstreamTask, error)
    // 取回上游任务的成功产物 URL 列表。
    Result(ctx context.Context, upstreamTaskID string) ([]string, error)
}
```

- 状态映射：mock 上游的 `Status` 必须是第 5 节的状态枚举之一，避免引入第二套状态词表。
- 结果 URL 必须经服务端 SSRF 白名单（`service.SafeProxyHTTPClient`，安全边界 §5.4）后再回填（#22）。
- 载荷最小化：只传完成该任务所需参数（渠道、工作流标识、参考图短时 URL、输出目标节点 ID），**不含**学校账号、Cookie、设备令牌、模型密钥（安全边界 §3.3）。

## 7. 鉴权、秘密与公开视图

- 任务查询与回填走房间成员鉴权；设备侧走独立设备令牌（第 2 节）。
- 对外公开视图 `model.SchoolTaskPublicView`（`SchoolTask.PublicView()`）只含：`id`、`roomId`、`submittedBy`、`executorId`、`clientTaskId`、`channel`、`status`、`error`。可区分 **submitter / device account / status / room**，且**不含** `upstreamTaskId`、学校凭据、Cookie、设备令牌、工作流明文 ID。
- 与协作广播一致：秘密字段永不进入公共图 / 广播（契约 §2.4、安全边界 §5.1）。凭据类键由 `contract.IsForbiddenBroadcastKey` 强制拒绝。

## 8. 错误码

复用契约错误码，不新增第二套 HTTP 映射：

| 场景 | code | HTTP |
|---|---|---|
| 参数/渠道/参考图数量/秒数越界 | `invalid_payload` | 400 |
| viewer 提交、设备越权 | `forbidden` | 403 |
| 任务不存在或非本人房间 | `not_found` | 404 |
| 非法状态迁移、`upstreamTaskId` 已绑定不可替换 | `version_conflict` | 409 |
| `clientTaskId` 复用于不同参数 | `operation_id_reused` | 409 |

## 9. 开放点与未解决风险

- **O1**：学校设备的具体 `OwnerScope` 取值（受控系统级归属的枚举值）待 #19 决策（安全边界 R4）。
- **O2**：设备令牌的签发/轮换/吊销流程待 #19/#28 落地，本契约只引用现有 Comfy Bridge 先例。
- **O3**：`school_` 表结构与索引（唯一键 `(roomId, submittedBy, clientTaskId)`）待 #18 落地；本契约不新增表。
- **O4**：H3 工作流字段的别名/单字段兼容细节（`ref_image` 别名场景）待 #21 对齐。
- **R1**：真实学校工作流与凭据不在本仓库、不在本任务范围内；任何真实设备/凭据操作需单独授权。
- **R2**：mock 上游接口为定义层，尚无生产实现；#18/#22 需用假数据实现并补集成测试。
