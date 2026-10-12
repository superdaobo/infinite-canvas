# 协作 v1 数据契约（C00-02 / W0 开工门槛）

- **协议版本**：`protocolVersion = "1"`（下文称 collab v1）
- **适用范围**：`superdaobo/infinite-canvas` 原生画布的多人协作、学校 H3 共享生成、Agent/模型/存储能力保留。
- **规范代码**：`internal/collab/contract/contract.go`（纯校验，无副作用）；测试 `internal/collab/contract/contract_test.go`。
- **基线 SHA**：`0a5e3b0b619eb11f7806e1ee28e658e4679a2bc5`。
- **不变量**：本契约是**新增独立文件/包**，不修改 `router/router.go`、`repository/db.go`、`web/src/app/(user)/canvas/types.ts`、`canvas-client-page.tsx`、`use-canvas-store.ts` 等热点文件。参考仓库（Vite/React）只借用协议语义，不照搬前端。

本文与 Go 校验函数一一对应：字段矩阵对应 `ValidateNode` / `ClassifyMetadataField`，操作语义对应 `ValidateOperation` / `ValidateEnvelope`，错误码对应 `StatusFor`，H3 语义对应 `H3Reference*`。任何实现必须同时通过 `go test ./internal/collab/...`。

---

## 1. 节点种类与公共结构

8 类节点与 `types.ts` 的 `CanvasNodeType` 一一对应：

| kind | 说明 | 备注 |
|---|---|---|
| `image` | 图片节点 | 生成/编辑/参考图 |
| `panorama` | 全景节点 | 等距柱状投影 |
| `video` | 视频节点 | 首尾帧、多图引用 |
| `audio` | 音频节点 | 语音/参考音频 |
| `text` | 文本节点 | 必填 `content` |
| `group` | 分组节点 | 仅布局容器，无连线语义 |
| `config` | 配置节点 | 必填 `content`，且必须是 JSON 对象 |
| `director` | 导演台节点 | 承载 `directorProject` |

公共图节点（`Node`）：

```json
{
  "id": "img_5Kd9xQ2mNp7",
  "kind": "image",
  "title": "首帧参考",
  "position": { "x": 120, "y": -40 },
  "width": 1024,
  "height": 1024,
  "version": 3,
  "metadata": { }
}
```

**ID 规则**（`ValidID`）：长度 4–64，字符集 `[A-Za-z0-9_-]`。可容纳当前前端 `nanoid()` 与 UUID（带 `-`）两种形态，例如 `img_5Kd9xQ2mNp7`、`550e8400-e29b-41d4-a716-446655440000`。

**连线（Edge）**：沿用原版 `CanvasConnection`，是 `fromNodeId -> toNodeId` 的有向边，v1 不引入参考仓库的端口模型；图片/音频等类型化引用由节点 `metadata`（如 `references`、`firstFrameNodeId`、`referenceAudioNodeId`）表达。

```json
{ "id": "edg_1Ab2Cd3Ef4", "fromNodeId": "txt_3Rt7yU1iOp5", "toNodeId": "img_5Kd9xQ2mNp7", "version": 2 }
```

---

## 2. 字段可见性矩阵（public / private / local）

三级可见性：

- **public**：进入房间公共图，随快照与增量广播，所有成员可见可编辑。
- **private**：按 `owner` 隔离持久化，仅本人可见，**绝不广播**给其他成员。
- **local**：仅浏览器本地/内存，**不落库、不广播**。

### 2.1 通用节点字段（所有 kind）

| 字段 | 等级 | 说明 |
|---|---|---|
| `id` | public | 不可变 |
| `kind` | public | 不可变，8 类之一 |
| `title` | public | ≤ 512 rune |
| `position.x/y` | public | 有限数值 |
| `width` / `height` | public | 正有限数值 |
| `version` | public | ≥ 1，冲突检测依据 |

### 2.2 metadata 字段矩阵

下表覆盖 `types.ts::CanvasNodeMetadata` 的全部字段（`FieldClass` 白名单）。**未列出的旧字段默认按 public 处理**（向后兼容），但凭据类键一律拒绝（见 2.4）。

| metadata 字段 | 等级 | 主要节点 | 说明 |
|---|---|---|---|
| `content` | public | text / config | text 必填；config 必填且为 JSON 对象 |
| `groupId` | public | 全部 | 分组归属 |
| `prompt` | public | image/video/audio/director | 共享创作意图 |
| `negativePrompt` | public | image | |
| `excludeUpstreamText` | public | 全部 | 上游文本是否排除 |
| `fontSize` | public | text | 展示字号 |
| `generationMode` / `generationType` | public | image | |
| `model` | public | 全部 | 模型名（非密钥） |
| `workflowRef` | public | 全部 | `{scope,channelId,kind,workflowId}`，不含密钥 |
| `channelId` | public | 全部 | 渠道标识，非密钥 |
| `size` / `quality` / `count` | public | image | |
| `seconds` / `vquality` / `mode` | public | video | |
| `generateAudio` / `characterOrientation` / `watermark` | public | video | |
| `audioVoice`/`audioFormat`/`audioSpeed`/`audioInstructions` | public | audio | |
| `grokTts*` / `glmTts*` / `mimoTts*` / `geminiTtsVoice` | public | audio | |
| `mimoVoiceDesignPrompt` | public | audio | |
| `mimoVoiceCloneAudioNodeId` / `referenceAudioNodeId` | public | audio | 引用节点 ID |
| `references` | public | 全部 | 引用节点 ID 列表 |
| `naturalWidth` / `naturalHeight` / `freeResize` | public | image | |
| `isBatchRoot`/`batchRootId`/`batchChildIds`/`batchUsesReferenceImages` | public | image | 批量结构 |
| `primaryImageId` / `imageBatchExpanded` | public | image | |
| `storageKey` / `mimeType` / `bytes` / `durationMs` | public | 媒体 | 媒体引用；鉴权在媒体层，不靠隐藏 key |
| `taskBackend` | public | 全部 | `account` / `workflow` |
| `firstFrameNodeId` / `lastFrameNodeId` | public | video | |
| `multiShot` / `shotType` | public | video/director | |
| `klingImageNodeIds` / `klingMultiPrompt` / `klingElementList` | public | video | |
| `cameraControl` | public | video/director | |
| `panoramaSourcePrompt`/`panoramaFinalPrompt`/`panoramaProjection` | public | panorama | |
| `directorProject` | public | director | 大对象，见第 8 节开放点 |
| **`composerContent`** | **private** | 全部 | 未确认草稿，owner 独占 |
| **`errorDetails`** | **private** | 全部 | 可能含服务端敏感信息 |
| **`clientTaskId`** | **private** | 全部 | 提交端任务关联 |
| **`imageTaskId`/`imageTaskResultId`** | **private** | image | 账号级任务回执 |
| **`audioTaskId`/`audioTaskResultId`** | **private** | audio | 账号级任务回执 |
| **`videoTaskId`/`videoTaskVideoId`/`videoTaskTranslation`** | **private** | video | 账号级任务回执 |
| **`status`** | **local** | 全部 | 运行时状态，不广播 |
| **`progress`** | **local** | 全部 | 运行时进度 |
| **`startedAt`** | **local** | 全部 | 运行时时间戳 |

### 2.3 节点外私有数据（不进公共图）

以下数据**不属于节点公共图**，必须按 `owner` 独立存储、绝不广播：

- Agent 会话：`CanvasAssistantSession`（含 `messages`、`protocolMessages`、`agentState`、`codexThreadId` 等）→ **private**。
- 用户模型密钥 / API Key（当前前端本地保存、后端 `ModelChannel.APIKey`）→ **private**，任何情况下不进入广播。
- 每用户未确认草稿、撤销栈（undo/redo）→ **private**（撤销为按用户隔离的历史）。
- 视口、选中态、光标、拖拽中状态 → **local**。

### 2.4 凭据类键强制拒绝

`ValidateNode` / `ValidateOperation` 对任何 metadata/fields 键执行 `IsForbiddenBroadcastKey`：归一化后精确命中或包含敏感片段即拒绝，返回 `forbidden_field`。保留键包括：`apiKey`/`api_key`、`authorization`、`token`（含以 token 结尾）、`secret`、`password`、`cookie`、`sessionId`、`credential`、`privateKey`、`masterKey`、`jwt` 等。这样即使前端误把密钥塞进节点，也无法通过公共图广播。

---

## 3. 8 类节点真实样例

> 下列样例同时作为 `contract_test.go::sampleNodes` 的输入，均通过 `ValidateNode`。

### 3.1 image

```json
{
  "id": "img_5Kd9xQ2mNp7", "kind": "image", "title": "首帧参考", "version": 3,
  "position": { "x": 120, "y": -40 }, "width": 1024, "height": 1024,
  "metadata": {
    "prompt": "赛博朋克城市夜景，霓虹倒影", "negativePrompt": "模糊, 低质量",
    "model": "seedream-4.0", "channelId": "channel_zh_01",
    "size": "16:9", "quality": "high", "count": 1,
    "generationMode": "image", "generationType": "generation",
    "storageKey": "rooms/r1/nodes/img_5Kd9xQ2mNp7.png", "mimeType": "image/png", "bytes": 204800,
    "naturalWidth": 1024, "naturalHeight": 1024, "freeResize": false
  }
}
```

private（owner 独占，单独存储）：`imageTaskId`、`imageTaskResultId`、`clientTaskId`、`composerContent`、`errorDetails`。local：`status`、`progress`、`startedAt`。

### 3.2 panorama

```json
{
  "id": "pano_7Qw3eR9tYb1", "kind": "panorama", "title": "360 全景", "version": 2,
  "position": { "x": 1500, "y": 0 }, "width": 2048, "height": 1024,
  "metadata": {
    "panoramaSourcePrompt": "雪山日出全景", "panoramaFinalPrompt": "雪山日出全景，8k",
    "panoramaProjection": "equirectangular", "storageKey": "rooms/r1/nodes/pano.png"
  }
}
```

### 3.3 video

```json
{
  "id": "vid_2Mn8bV4cXz6", "kind": "video", "title": "15s 成片", "version": 1,
  "position": { "x": 2600, "y": 0 }, "width": 1280, "height": 720,
  "metadata": {
    "prompt": "镜头推进", "model": "minimax_h3_image_audio_to_video_v2_15s",
    "seconds": "15", "vquality": "768p", "generateAudio": "true", "watermark": "false",
    "firstFrameNodeId": "img_5Kd9xQ2mNp7", "storageKey": "rooms/r1/nodes/vid.mp4", "durationMs": 15000
  }
}
```

### 3.4 audio

```json
{
  "id": "aud_9Pl4kH6jRt2", "kind": "audio", "title": "旁白", "version": 4,
  "position": { "x": 2600, "y": 800 }, "width": 480, "height": 120,
  "metadata": {
    "audioVoice": "zh_female_01", "audioFormat": "mp3", "audioSpeed": "1.0",
    "audioInstructions": "沉稳", "storageKey": "rooms/r1/nodes/aud.mp3", "durationMs": 8200
  }
}
```

### 3.5 text

```json
{
  "id": "txt_3Rt7yU1iOp5", "kind": "text", "title": "提示词草稿", "version": 5,
  "position": { "x": -400, "y": 0 }, "width": 360, "height": 200,
  "metadata": { "content": "黄昏下的海边小镇，暖色调", "fontSize": 14 }
}
```

`content` 为 text 节点必填字段。

### 3.6 group

```json
{
  "id": "grp_6Yu2iO8pAs3", "kind": "group", "title": "第一幕", "version": 1,
  "position": { "x": -600, "y": -200 }, "width": 900, "height": 700,
  "metadata": { "groupId": "grp_6Yu2iO8pAs3" }
}
```

### 3.7 config

```json
{
  "id": "cfg_8Io3pA5sDf9", "kind": "config", "title": "生成配置", "version": 2,
  "position": { "x": -400, "y": 400 }, "width": 320, "height": 240,
  "metadata": { "content": "{\"size\":\"16:9\",\"quality\":\"high\",\"count\":1}" }
}
```

`content` 必须是 JSON 对象字符串；否则返回 `invalid_payload`。

### 3.8 director

```json
{
  "id": "dir_1As4dF6gHj7", "kind": "director", "title": "导演台", "version": 1,
  "position": { "x": 800, "y": -600 }, "width": 960, "height": 540,
  "metadata": {
    "directorProject": { "shots": [] },
    "primaryImageId": "img_5Kd9xQ2mNp7",
    "references": ["img_5Kd9xQ2mNp7"],
    "cameraControl": { "enabled": true, "camera": "dolly", "lens": "35mm", "focalLength": 35, "aperture": 2.8 },
    "multiShot": "true",
    "klingImageNodeIds": ["img_5Kd9xQ2mNp7"]
  }
}
```

---

## 4. 操作、事务与幂等

### 4.1 操作信封

```json
{
  "protocolVersion": "1",
  "roomId": "room_1Ab2Cd3Ef4",
  "operationId": "op_9Zy8Xw7Vu6",
  "operations": [
    { "type": "update", "id": "img_5Kd9xQ2mNp7", "version": 3, "fields": { "title": "新标题" } }
  ]
}
```

### 4.2 操作类型

| type | 必需字段 | 语义 |
|---|---|---|
| `create` | `node`（`version` 必须为 0） | 新建节点，落库后 `version=1` |
| `update` | `id`、`version`（expectedNodeVersion ≥1）、非空 `fields` | 增量更新，`fields` 只允许 public/local 字段 |
| `delete` | `id`、`version`（≥1） | 删除节点 |
| `connect` | `edge`（`version=0` 新建；>0 期望版本） | 新建/更新连线 |
| `disconnect` | `id`、`version`（≥1） | 删除连线 |

### 4.3 原子事务语义

- 一个 `operations` 批次在**单个数据库事务**内全部应用或全部回滚；不存在部分成功。
- 批次内**不得重复修改同一目标**（同一节点/连线 ID），否则返回 `duplicate_target`。
- 版本冲突：任一 `update`/`delete`/`disconnect` 的 `expectedNodeVersion` 与服务端当前版本不一致 → 整批回滚，返回 `version_conflict`（`409`），并在 `details.node` 携带最新节点，客户端据此保留草稿。
- 节点已被删除 → `node_deleted`（`409`，`details.deleted=true`）。
- 提交成功后，房间 `revision` 恰好 +1，广播一次 `changes`；**确认与广播只在事务提交后发生**。
- 批次上限 `MaxOperationsPerBatch = 200`，超出返回 `payload_too_large`。

### 4.4 幂等响应规则

- 幂等键为 `(roomId, userId, operationId)`；服务端持久化 `receipt`，记录请求摘要与结果。
- 相同 `operationId` + 相同请求体 → 直接返回已存结果（不重复执行、不重复广播）。
- 相同 `operationId` + 不同请求体 → `operation_id_reused`（`409`）。
- 客户端断线重发、网络超时重试均依赖此规则；重试不得产生重复学校执行或重复节点。

### 4.5 错误码表

| code | HTTP | 触发场景 |
|---|---|---|
| `invalid_payload` | 400 | 字段缺失/类型错误/ID 非法/宽高非正/版本缺失/未知 kind/未知 op/空批次 |
| `unauthorized` | 401 | JWT 缺失、过期或无效 |
| `forbidden` | 403 | viewer 只读、非 owner 管理成员、非本人私有节点 |
| `not_found` | 404 | 房间/节点/私有节点不存在或无权限 |
| `forbidden_field` | 400 | metadata/fields 含凭据类保留键（模型密钥等） |
| `version_conflict` | 409 | expectedNodeVersion 与当前版本不一致 |
| `node_exists` | 409 | create 的节点 ID 已存在 |
| `node_deleted` | 409 | 更新/删除已删除节点 |
| `edge_changed` | 409 | 连线版本已变化 |
| `operation_id_reused` | 409 | operationId 复用于不同请求体 |
| `duplicate_target` | 400 | 同批次重复修改同一目标 / 连线 ID 重复 |
| `cycle_detected` | 400 | 连线成环 |
| `payload_too_large` | 413 | 单批操作数或单帧负载超限 |
| `too_many_connections` | 429 | 房间在线连接达上限 |
| `protocol_version_mismatch` | 400 | 协议版本不匹配（WS 以 4401 关闭） |
| `private_node_forbidden` | 403 | 访问/连接非本人私有节点 |

错误响应统一结构：`{ "code": "<code>", "message": "<中文>", "details": { ... } }`，`StatusFor(code)` 给出 HTTP 状态。

---

## 5. WebSocket 事件

### 5.1 身份绑定与握手

浏览器 WebSocket 无法自定义 `Authorization` 头，因此 **JWT 不放进 URL**（避免日志泄漏）。流程：

1. 客户端携带主 Go 账号 JWT 调用 `POST /api/collab/rooms/{roomId}/ws-ticket`。
2. 服务端校验 JWT（HS256，`config.Cfg.JWTSecret`，`TokenClaims{userId,username,role}`），确认房间成员权限后签发**短时效、单次使用、绑定 userId+roomId** 的 `ticket`。
3. 客户端建立 WS 后发送首帧 `hello`：

```json
{ "type": "hello", "protocolVersion": "1", "roomId": "room_1Ab2Cd3Ef4", "ticket": "<opaque>" }
```

4. 服务端校验 ticket 与协议版本，绑定 `socket.userId`；版本不匹配以 `4401` 关闭并携带 `protocol_version_mismatch`。ticket 一次性消费，重连需重新签发。
5. 会话失效（登出、权限撤销、分享过期）→ 服务端主动以 `4401` 关闭该用户全部连接，并中止其进行中的请求。

> 备选：以 `Sec-WebSocket-Protocol` 子协议携带 ticket，语义同上；推荐首帧 `hello`，便于统一版本校验。

### 5.2 事件类型

| type | 方向 | 负载 |
|---|---|---|
| `hello` | C→S | `{protocolVersion, roomId, ticket}` |
| `snapshot-start` | S→C | `{userId, room:{id,title,revision,role}}` |
| `snapshot-node` | S→C | `{node, ownPrivate:bool}`；私有节点仅推送给本人，他人收到脱敏占位 |
| `snapshot-edge` | S→C | `{edge}` |
| `snapshot-end` | S→C | `{revision}` |
| `changes` | S→C | `{operationId, historyId, revision, changes:[...]}` |
| `presence` | S→C | `{count, members:[{userId,username}]}`（不含私有数据） |
| `cursors` | S→C | 批量 `{cursors:[{userId,username,position}]}`，节流合并 |
| `error` | S→C | `{code, message, details}` |
| `ping`/`pong` | 双向 | 心跳；超时判定掉线 |

`changes[].type` 取值：`upsert`（含 `node`）、`delete`（含 `id`）、`edge-upsert`（含 `edge`）、`edge-delete`（含 `id`）。

**快照分帧**：节点/连线逐条发送 `snapshot-node`/`snapshot-edge`，避免超大房间产生单个巨帧；快照期间的新操作按 `revision` 补发 `changes`。

**私有数据隔离**：快照与 `changes` 中，`ownPrivate=false` 的私有节点只发送脱敏占位（`{id, kind:"private", position, width, height, title:"隐私节点", content:"", version}`），**绝不发送** private metadata、Agent 会话或密钥。

### 5.3 断线恢复

客户端记录最后 `revision`，重连后以 `snapshot` 全量重建；未确认操作以原 `operationId` 重发，服务端按幂等规则返回既有结果或执行。

---

## 6. 学校 H3 渠道有序参考图语义

**仅约束学校 H3 渠道**（AutoDL 协议下 H3 视频模型族，如 `minimax_h3_image_audio_to_video_v2_15s`、`minimax_h3_b99_*`、`minimax_h3_lightx2v_*` 等）；其他模型渠道保持各自既有映射，**不受本约束限制**。

- 参考图为**有序数组** `referenceImages`（0 基）。
- 固定映射：第 `i` 张 → 工作流字段 `ref_image_i`（`i = 0..8`）。若工作流只暴露无编号单字段，则 `i=0` 使用别名 `ref_image`。
- 数量范围：`H3MinReferenceImages = 1`，`H3MaxReferenceImages = 9`；越界返回 `invalid_payload`。下标越界（≥9 或 <0）同样拒绝。
- 顺序即语义：`ref_image_0` 是第 1 张，`ref_image_8` 是第 9 张，提交端不得重排。
- 对应代码：`H3ReferenceField(index)`、`H3ReferenceAlias(index)`、`ValidateH3ReferenceCount(count)`、`H3ReferenceMapping(order)`。

---

## 7. 鉴权与隐私（字段级规定）

- **主 Go 账号 JWT**：沿用 `service.TokenClaims{userId,username,role}` + `jwt.RegisteredClaims`，HS256，密钥 `config.Cfg.JWTSecret`。协作 REST 与 WS ticket 签发共用同一 JWT。
- **WS 身份绑定**：见 5.1，`socket.userId` 由 ticket 决定，客户端无法伪造；每次操作仍以服务端绑定的 `userId` 作为归属与幂等键的一部分。
- **角色**：`owner` / `editor` / `viewer`；viewer 只读，非 owner 不能管理分享与成员。
- **不得广播的字段级规定**：
  - 模型密钥、`apiKey`、`authorization`、Cookie、session、任何凭据 → 永不进入公共图，`IsForbiddenBroadcastKey` 强制拒绝。
  - 私有 Agent 聊天（`CanvasAssistantSession` 全量）→ private，仅 owner 可见。
  - private metadata（2.2 表）→ 仅 owner；他人收到脱敏占位。
  - 私有节点：非 owner 只能看到占位，不能读取/修改/连接（连接私有节点需同 owner，否则 `private_node_forbidden`）。

---

## 8. 向后兼容与开放点

### 8.1 向后兼容

- **旧单人本地项目**：仍是合法输入。缺失 `version` → 导入时视为 `1`；缺失可见性标注的旧 metadata 字段 → 默认 public；未知字段保留为 public（不丢数据）。
- **旧单人模式**：协作能力为新增路径，单人本地保存/账号同步逻辑不变；未登录仍本地保存。
- **协议演进**：`protocolVersion` 不匹配时服务端拒绝并要求客户端刷新，不做隐式降级；未来新增字段必须可选且默认值保持旧语义。

### 8.2 需主控/用户拍板的开放点

1. **`status`/`progress` 是否广播**：当前定为 local（不广播，避免每个成员的生成进度互相打扰、避免泄露个人任务态）。若产品希望成员看到"某节点正在生成"，需改为 public 并定义节流。
2. **`directorProject` 体积**：当前为 public 大对象。若单个导演台项目很大，需改为 `storageKey` 外链 + 公共图只存引用（涉及媒体层授权，属 C04 范围）。
3. **`storageKey` 的访问控制**：契约把 `storageKey` 列为 public（引用非机密），但真正的媒体下载鉴权必须在媒体接口层实现（C04-01 #20），本契约不替代该鉴权。
4. **连线端口模型**：v1 采用 `fromNodeId->toNodeId` 简单模型；若学校 H3 需要"图片输入槽/音频输入槽"的强类型端口，需与 #9/#21 对齐后扩展（会改变 Edge 结构，需版本化）。
5. **撤销粒度**：契约规定撤销按用户隔离（private history），跨用户协作撤销的冲突提示属 #11/#15 范围。

### 8.3 未解决风险

- 参考仓库为 Vite/React，协议语义可借鉴但前端实现不可照搬；本契约只固定数据与协议层。
- 学校真实渠道、设备、凭据操作需单独授权，本任务不涉及。
- `types.ts` 的 `metadata` 仍在演进；本契约以当前 SHA 快照为准，后续新增字段默认 public，需在实现 Issue 中同步白名单。
