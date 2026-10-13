// Package contract 定义协作 v1 的跨端数据契约：节点种类、字段可见性分类、
// 操作与事务语义、幂等键、错误码、WebSocket 事件以及学校 H3 渠道的
// 有序参考图输入语义。
//
// 本包只做纯校验，不依赖数据库、路由或前端；热点文件（router、repository、
// canvas types.ts 等）不得引用或修改。契约的规范文本见 docs/collab-v1/contract.md。
package contract

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// ProtocolVersion 是协作 v1 的协议版本号。客户端在 WS 握手和每次操作信封
// 中都必须携带；不匹配时服务端返回 CodeProtocolMismatch。
const ProtocolVersion = "1"

// 单次操作批次的边界。超过上限返回 CodePayloadTooLarge。
const (
	MaxOperationsPerBatch = 200
	MaxNodeTitleRunes     = 512
	MaxIDLength           = 64
	MinIDLength           = 4
	// MaxNodeMetadataBytes 是单个节点 metadata 序列化为 JSON 后的最大字节数（1 MiB）。
	// 依据：directorProject 当前是 public 大对象（contract.md 8.2 开放点 2），需要
	// 容纳正常导演台项目；超过 1 MiB 的单节点 metadata 视为异常负载，返回
	// CodeInvalidPayload（invalid_payload），避免任意已登录账号用超大 metadata 消耗内存。
	MaxNodeMetadataBytes = 1 << 20
)

// NodeKind 是画布节点种类，与 web/src/app/(user)/canvas/types.ts 的
// CanvasNodeType 一一对应。
type NodeKind string

const (
	NodeKindImage    NodeKind = "image"
	NodeKindPanorama NodeKind = "panorama"
	NodeKindVideo    NodeKind = "video"
	NodeKindAudio    NodeKind = "audio"
	NodeKindText     NodeKind = "text"
	NodeKindGroup    NodeKind = "group"
	NodeKindConfig   NodeKind = "config"
	NodeKindDirector NodeKind = "director"
)

// NodeKinds 按契约顺序返回全部 8 类节点。
func NodeKinds() []NodeKind {
	return []NodeKind{
		NodeKindImage, NodeKindPanorama, NodeKindVideo, NodeKindAudio,
		NodeKindText, NodeKindGroup, NodeKindConfig, NodeKindDirector,
	}
}

// Valid 判断是否为契约内的 8 类节点。
func (k NodeKind) Valid() bool {
	switch k {
	case NodeKindImage, NodeKindPanorama, NodeKindVideo, NodeKindAudio,
		NodeKindText, NodeKindGroup, NodeKindConfig, NodeKindDirector:
		return true
	default:
		return false
	}
}

// FieldClass 描述一个字段在协作中的可见性等级。
type FieldClass string

const (
	// ClassPublic：随房间公共图快照与增量广播，所有成员可见。
	ClassPublic FieldClass = "public"
	// ClassPrivate：按 owner 持久化，仅本人可见，绝不广播给其他成员。
	ClassPrivate FieldClass = "private"
	// ClassLocal：仅浏览器本地/内存，不落库、不广播。
	ClassLocal FieldClass = "local"
)

// Position 是画布世界坐标。
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Node 是协作公共图中的一个节点。Metadata 里的字段按 ClassifyMetadataField
// 分为 public/private/local；public 字段进入公共图，private 字段单独按 owner 存储。
type Node struct {
	ID       string         `json:"id"`
	Kind     NodeKind       `json:"kind"`
	Title    string         `json:"title"`
	Position Position       `json:"position"`
	Width    float64        `json:"width"`
	Height   float64        `json:"height"`
	Version  int            `json:"version"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Edge 对应原版 CanvasConnection：fromNodeId -> toNodeId 的有向连线。
// v1 不引入参考仓库的端口模型，类型化引用（图片/音频）由节点 metadata 表达。
type Edge struct {
	ID         string `json:"id"`
	FromNodeID string `json:"fromNodeId"`
	ToNodeID   string `json:"toNodeId"`
	Version    int    `json:"version"`
}

// OpType 是操作类型，对应参考协议的 create/update/delete/connect/disconnect。
type OpType string

const (
	OpCreate     OpType = "create"
	OpUpdate     OpType = "update"
	OpDelete     OpType = "delete"
	OpConnect    OpType = "connect"
	OpDisconnect OpType = "disconnect"
)

// Operation 是批次内的单个原子操作。
type Operation struct {
	Type OpType `json:"type"`
	// Node 仅在 create 时使用，且 Version 必须为 0。
	Node *Node `json:"node,omitempty"`
	// ID 是 update/delete/disconnect 的目标节点/连线 ID。
	ID string `json:"id,omitempty"`
	// Version 是 expectedNodeVersion（update/delete/disconnect 必填且 >=1）；
	// connect 时为已存在连线的期望版本，0 表示新建。
	Version *int `json:"version,omitempty"`
	// Fields 是 update 的增量字段（仅允许 public/local 字段，禁止凭据字段）。
	Fields map[string]any `json:"fields,omitempty"`
	// Edge 仅在 connect 时使用。
	Edge *Edge `json:"edge,omitempty"`
}

// Envelope 是一次幂等提交的操作信封。
type Envelope struct {
	ProtocolVersion string      `json:"protocolVersion"`
	RoomID          string      `json:"roomId"`
	OperationID     string      `json:"operationId"`
	Operations      []Operation `json:"operations"`
}

// 错误码。字符串码用于跨端稳定匹配；StatusFor 给出对应的 HTTP 状态。
const (
	CodeInvalidPayload       = "invalid_payload"
	CodeUnauthorized         = "unauthorized"
	CodeForbidden            = "forbidden"
	CodeNotFound             = "not_found"
	CodeForbiddenField       = "forbidden_field"
	CodeVersionConflict      = "version_conflict"
	CodeNodeExists           = "node_exists"
	CodeNodeDeleted          = "node_deleted"
	CodeEdgeChanged          = "edge_changed"
	CodeOperationIDReused    = "operation_id_reused"
	CodeRoomNotEmpty         = "room_not_empty"
	CodeDuplicateTarget      = "duplicate_target"
	CodeCycleDetected        = "cycle_detected"
	CodePayloadTooLarge      = "payload_too_large"
	CodeTooManyConnections   = "too_many_connections"
	CodeProtocolMismatch     = "protocol_version_mismatch"
	CodePrivateNodeForbidden = "private_node_forbidden"
)

// StatusFor 返回错误码对应的 HTTP 状态码；未知码回落到 400。
func StatusFor(code string) int {
	switch code {
	case CodeUnauthorized:
		return 401
	case CodeForbidden, CodePrivateNodeForbidden:
		return 403
	case CodeNotFound:
		return 404
	case CodeVersionConflict, CodeNodeExists, CodeNodeDeleted, CodeEdgeChanged, CodeOperationIDReused, CodeRoomNotEmpty:
		return 409
	case CodePayloadTooLarge:
		return 413
	case CodeTooManyConnections:
		return 429
	default:
		return 400
	}
}

// Error 是契约校验错误。用 errors.As 取回后可按 Code/Status 处理。
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Status 返回该错误对应的 HTTP 状态码。
func (e *Error) Status() int { return StatusFor(e.Code) }

func newError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// --- 字段可见性分类 ---

// publicMetadataFields 是允许进入公共图的 metadata 白名单字段。
var publicMetadataFields = fieldSet(
	"content", "groupId", "prompt", "excludeUpstreamText", "fontSize",
	"generationMode", "generationType", "model", "workflowRef", "channelId",
	"size", "quality", "count", "seconds", "vquality", "mode", "negativePrompt",
	"generateAudio", "characterOrientation", "watermark",
	"audioVoice", "audioFormat", "audioSpeed", "audioInstructions",
	"grokTtsVoice", "grokTtsLanguage", "grokTtsFormat", "grokTtsSpeed",
	"glmTtsVoice", "glmTtsFormat", "glmTtsSpeed",
	"mimoTtsVoice", "mimoTtsFormat", "mimoVoiceDesignPrompt", "geminiTtsVoice",
	"mimoVoiceCloneAudioNodeId", "referenceAudioNodeId", "references",
	"naturalWidth", "naturalHeight", "freeResize",
	"isBatchRoot", "batchRootId", "batchChildIds", "batchUsesReferenceImages",
	"primaryImageId", "imageBatchExpanded",
	"storageKey", "mimeType", "bytes", "durationMs", "taskBackend",
	"firstFrameNodeId", "lastFrameNodeId", "multiShot", "shotType",
	"klingImageNodeIds", "klingMultiPrompt", "klingElementList", "cameraControl",
	"panoramaSourcePrompt", "panoramaFinalPrompt", "panoramaProjection",
	"directorProject",
)

// privateMetadataFields 是按 owner 隔离、绝不广播的 metadata 字段。
var privateMetadataFields = fieldSet(
	"composerContent", "errorDetails", "clientTaskId",
	"imageTaskId", "imageTaskResultId", "audioTaskId", "audioTaskResultId",
	"videoTaskId", "videoTaskVideoId", "videoTaskTranslation",
)

// localMetadataFields 是仅本地/内存、不落库不广播的 metadata 字段。
var localMetadataFields = fieldSet("status", "progress", "startedAt")

// forbiddenBroadcastKeys 是任何情况下都不得进入公共图的凭据类键名。
// 命中即返回 CodeForbiddenField，防止模型密钥、Agent 会话、Cookie 等被广播。
var forbiddenBroadcastKeys = fieldSet(
	"apikey", "api_key", "authorization", "auth", "token", "accesstoken",
	"refreshtoken", "secret", "clientsecret", "password", "passwd", "cookie",
	"setcookie", "session", "sessionid", "credential", "credentials",
	"privatekey", "masterkey", "signingkey", "jwt", "bearertoken",
)

func fieldSet(names ...string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[normalizeFieldName(name)] = true
	}
	return set
}

// normalizeFieldName 去掉分隔符并小写，使 apiKey/api_key/API-KEY 归一到同一键。
func normalizeFieldName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// forbiddenKeySubstrings 是组合键名中的敏感片段，例如 secretToken、myApiKey。
var forbiddenKeySubstrings = []string{
	"apikey", "authorization", "password", "credential", "secret",
	"cookie", "sessionid", "privatekey", "masterkey",
}

// IsForbiddenBroadcastKey 判断键名是否为凭据类保留键。先做归一化精确匹配，
// 再匹配组合键名中的敏感片段，以及以 token 结尾的键。
func IsForbiddenBroadcastKey(name string) bool {
	key := normalizeFieldName(name)
	if forbiddenBroadcastKeys[key] {
		return true
	}
	for _, fragment := range forbiddenKeySubstrings {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return strings.HasSuffix(key, "token")
}

// ClassifyMetadataField 返回 metadata 字段的可见性等级。已知白名单字段按其
// 等级返回；未知字段默认 public（向后兼容旧项目），凭据类键由 ValidateNode 拒绝。
func ClassifyMetadataField(name string) FieldClass {
	key := normalizeFieldName(name)
	switch {
	case privateMetadataFields[key]:
		return ClassPrivate
	case localMetadataFields[key]:
		return ClassLocal
	case publicMetadataFields[key]:
		return ClassPublic
	default:
		return ClassPublic
	}
}

// requiredMetadata 是各类节点的必填公共 metadata 字段。
var requiredMetadata = map[NodeKind][]string{
	NodeKindText:   {"content"},
	NodeKindConfig: {"content"},
}

// --- ID 与基础校验 ---

// ValidID 校验 ID 形如 nanoid/uuid：长度 4..64，仅 [A-Za-z0-9_-]。
func ValidID(id string) bool {
	if utf8.RuneCountInString(id) < MinIDLength || len(id) > MaxIDLength {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// ValidateNode 校验一个公共图节点，覆盖 8 类节点的公共字段与可见性规则。
func ValidateNode(node *Node) error {
	if node == nil {
		return newError(CodeInvalidPayload, "节点不能为空")
	}
	if !ValidID(node.ID) {
		return newError(CodeInvalidPayload, "节点 ID 非法")
	}
	if !node.Kind.Valid() {
		return newError(CodeInvalidPayload, fmt.Sprintf("未知节点种类 %q", node.Kind))
	}
	if utf8.RuneCountInString(node.Title) > MaxNodeTitleRunes {
		return newError(CodeInvalidPayload, "节点标题过长")
	}
	if !finite(node.Position.X) || !finite(node.Position.Y) {
		return newError(CodeInvalidPayload, "节点坐标必须是有限数值")
	}
	if !finite(node.Width) || node.Width <= 0 || !finite(node.Height) || node.Height <= 0 {
		return newError(CodeInvalidPayload, "节点宽高必须是正有限数值")
	}
	if node.Version < 1 {
		return newError(CodeInvalidPayload, "节点版本必须 >= 1")
	}
	for key := range node.Metadata {
		if IsForbiddenBroadcastKey(key) {
			return newError(CodeForbiddenField, fmt.Sprintf("字段 %q 属于凭据/私有数据，禁止进入公共图", key))
		}
	}
	if err := validateMetadataSize(node.Metadata); err != nil {
		return err
	}
	for _, key := range requiredMetadata[node.Kind] {
		value, ok := node.Metadata[key]
		if !ok {
			return newError(CodeInvalidPayload, fmt.Sprintf("%s 节点缺少必填字段 %q", node.Kind, key))
		}
		if node.Kind == NodeKindConfig {
			if err := validateJSONObject(value); err != nil {
				return newError(CodeInvalidPayload, "config 节点的 content 必须是 JSON 对象")
			}
		}
	}
	return nil
}

func validateJSONObject(value any) error {
	var raw []byte
	switch typed := value.(type) {
	case string:
		raw = []byte(typed)
	case json.RawMessage:
		raw = typed
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return err
		}
		raw = encoded
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return fmt.Errorf("不是 JSON 对象")
	}
	return nil
}

// validateMetadataSize 限制单个节点 metadata 的序列化字节数，超限返回 invalid_payload。
// 空 metadata 直接通过，避免无意义的序列化开销。
func validateMetadataSize(metadata map[string]any) error {
	if len(metadata) == 0 {
		return nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return newError(CodeInvalidPayload, "metadata 无法序列化")
	}
	if len(encoded) > MaxNodeMetadataBytes {
		return newError(CodeInvalidPayload, fmt.Sprintf("节点 metadata 超过上限 %d 字节", MaxNodeMetadataBytes))
	}
	return nil
}

// --- 操作与批次校验 ---

// ValidateOperation 校验单个操作，返回带错误码的 *Error。
func ValidateOperation(op Operation) error {
	switch op.Type {
	case OpCreate:
		if op.Node == nil {
			return newError(CodeInvalidPayload, "create 必须携带 node")
		}
		if op.Node.Version != 0 {
			return newError(CodeInvalidPayload, "create 的新节点版本必须为 0")
		}
		// create 用版本 0 表达新节点，先按临时版本 1 做其余字段校验。
		candidate := *op.Node
		candidate.Version = 1
		return ValidateNode(&candidate)
	case OpUpdate:
		if !ValidID(op.ID) {
			return newError(CodeInvalidPayload, "update 目标 ID 非法")
		}
		if op.Version == nil || *op.Version < 1 {
			return newError(CodeInvalidPayload, "update 必须携带 expectedNodeVersion（>=1）")
		}
		if len(op.Fields) == 0 {
			return newError(CodeInvalidPayload, "update 的 fields 不能为空")
		}
		for key := range op.Fields {
			if IsForbiddenBroadcastKey(key) {
				return newError(CodeForbiddenField, fmt.Sprintf("字段 %q 属于凭据/私有数据，禁止写入公共图", key))
			}
		}
		if raw, ok := op.Fields["metadata"]; ok {
			metadata, ok := raw.(map[string]any)
			if !ok {
				return newError(CodeInvalidPayload, "update 的 metadata 必须是对象")
			}
			if err := validateMetadataSize(metadata); err != nil {
				return err
			}
		}
		return nil
	case OpDelete:
		if !ValidID(op.ID) {
			return newError(CodeInvalidPayload, "delete 目标 ID 非法")
		}
		if op.Version == nil || *op.Version < 1 {
			return newError(CodeInvalidPayload, "delete 必须携带 expectedNodeVersion（>=1）")
		}
		return nil
	case OpConnect:
		if op.Edge == nil {
			return newError(CodeInvalidPayload, "connect 必须携带 edge")
		}
		if !ValidID(op.Edge.ID) || !ValidID(op.Edge.FromNodeID) || !ValidID(op.Edge.ToNodeID) {
			return newError(CodeInvalidPayload, "connect 的连线 ID 或端点 ID 非法")
		}
		if op.Edge.FromNodeID == op.Edge.ToNodeID {
			return newError(CodeInvalidPayload, "连线不能指向自身")
		}
		if op.Edge.Version < 0 {
			return newError(CodeInvalidPayload, "连线版本不能为负")
		}
		return nil
	case OpDisconnect:
		if !ValidID(op.ID) {
			return newError(CodeInvalidPayload, "disconnect 目标 ID 非法")
		}
		if op.Version == nil || *op.Version < 1 {
			return newError(CodeInvalidPayload, "disconnect 必须携带 expectedVersion（>=1）")
		}
		return nil
	default:
		return newError(CodeInvalidPayload, fmt.Sprintf("未知操作类型 %q", op.Type))
	}
}

// ValidateEnvelope 校验一次幂等提交信封：协议版本、房间、幂等键、批次边界，
// 以及批次内不得重复修改同一目标（参考协议同款约束）。
func ValidateEnvelope(envelope Envelope) error {
	if envelope.ProtocolVersion != ProtocolVersion {
		return newError(CodeProtocolMismatch, fmt.Sprintf("协议版本不匹配，期望 %s", ProtocolVersion))
	}
	if !ValidID(envelope.RoomID) {
		return newError(CodeInvalidPayload, "房间 ID 非法")
	}
	if !ValidID(envelope.OperationID) {
		return newError(CodeInvalidPayload, "操作 ID（幂等键）非法")
	}
	if len(envelope.Operations) == 0 {
		return newError(CodeInvalidPayload, "操作批次不能为空")
	}
	if len(envelope.Operations) > MaxOperationsPerBatch {
		return newError(CodePayloadTooLarge, fmt.Sprintf("单批操作数超过上限 %d", MaxOperationsPerBatch))
	}
	targets := make(map[string]bool, len(envelope.Operations))
	for _, op := range envelope.Operations {
		if err := ValidateOperation(op); err != nil {
			return err
		}
		target := op.ID
		if op.Type == OpCreate && op.Node != nil {
			target = op.Node.ID
		}
		if op.Type == OpConnect && op.Edge != nil {
			target = op.Edge.ID
		}
		if targets[target] {
			return newError(CodeDuplicateTarget, fmt.Sprintf("同一批次不能重复修改 %q", target))
		}
		targets[target] = true
	}
	return nil
}

// --- 图结构校验 ---

// ValidateGraph 校验公共图的连线合法性：端点存在、无自环、无重复连线、
// 无环。edges 与 nodes 以 ID 为键。
func ValidateGraph(nodes map[string]Node, edges []Edge) error {
	seen := make(map[string]bool, len(edges))
	successors := make(map[string][]string)
	indegree := make(map[string]int)
	for id := range nodes {
		indegree[id] = 0
	}
	for _, edge := range edges {
		if seen[edge.ID] {
			return newError(CodeDuplicateTarget, fmt.Sprintf("连线 ID 重复：%s", edge.ID))
		}
		seen[edge.ID] = true
		if _, ok := nodes[edge.FromNodeID]; !ok {
			return newError(CodeInvalidPayload, fmt.Sprintf("连线 %s 的起点不存在", edge.ID))
		}
		if _, ok := nodes[edge.ToNodeID]; !ok {
			return newError(CodeInvalidPayload, fmt.Sprintf("连线 %s 的终点不存在", edge.ID))
		}
		if edge.FromNodeID == edge.ToNodeID {
			return newError(CodeInvalidPayload, "连线不能指向自身")
		}
		successors[edge.FromNodeID] = append(successors[edge.FromNodeID], edge.ToNodeID)
		indegree[edge.ToNodeID]++
	}
	queue := make([]string, 0, len(nodes))
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)
	visited := 0
	for index := 0; index < len(queue); index++ {
		visited++
		for _, next := range successors[queue[index]] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited != len(nodes) {
		return newError(CodeCycleDetected, "连线不能形成循环")
	}
	return nil
}

// --- 学校 H3 渠道有序参考图语义 ---

const (
	// H3MinReferenceImages 是学校 H3 渠道最少参考图数量。
	H3MinReferenceImages = 1
	// H3MaxReferenceImages 是学校 H3 渠道最多参考图数量。
	H3MaxReferenceImages = 9
	// h3ReferencePrefix 是 H3 工作流参考图字段前缀。
	h3ReferencePrefix = "ref_image"
)

// H3ReferenceField 返回 0 基下标对应的 H3 参考图字段名：index 0 -> ref_image_0。
func H3ReferenceField(index int) (string, error) {
	if index < 0 || index >= H3MaxReferenceImages {
		return "", newError(CodeInvalidPayload, fmt.Sprintf("H3 参考图下标越界：%d", index))
	}
	return fmt.Sprintf("%s_%d", h3ReferencePrefix, index), nil
}

// H3ReferenceAlias 返回下标 0 的无编号别名 ref_image（工作流只暴露单个字段时使用）。
func H3ReferenceAlias(index int) (string, bool) {
	if index == 0 {
		return h3ReferencePrefix, true
	}
	return "", false
}

// ValidateH3ReferenceCount 校验学校 H3 渠道参考图数量为 1..9。
// 该约束只作用于学校 H3 渠道，不限制其他模型渠道。
func ValidateH3ReferenceCount(count int) error {
	if count < H3MinReferenceImages || count > H3MaxReferenceImages {
		return newError(CodeInvalidPayload, fmt.Sprintf("学校 H3 渠道参考图数量必须在 %d..%d 之间，当前 %d",
			H3MinReferenceImages, H3MaxReferenceImages, count))
	}
	return nil
}

// H3ReferenceMapping 按固定顺序把参考图下标映射到字段名。order 中的第 i 个
// 元素是图片节点 ID，返回结果保留顺序；数量越界返回错误。
func H3ReferenceMapping(order []string) ([]string, error) {
	if err := ValidateH3ReferenceCount(len(order)); err != nil {
		return nil, err
	}
	fields := make([]string, 0, len(order))
	for index := range order {
		field, err := H3ReferenceField(index)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	return fields, nil
}
