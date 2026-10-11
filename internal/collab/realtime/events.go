// Package realtime 实现协作 v1 的 WebSocket 实时层：短时效单次 ticket 换连接、
// 房间成员鉴权、分帧快照、提交后变更广播、在线状态与协作光标。
//
// 设计要点（对照 docs/collab-v1/contract.md 第 5 节与 security-boundaries.md 1.2）：
//   - JWT 不放进 URL；客户端先用 JWT 换短时效单次 ticket，再在首帧 hello 里携带 ticket；
//   - 只有房间成员可连；viewer 可连但只读（写事件二次鉴权由 service 层把关）；
//   - 广播只包含公共图数据、成员公开信息与在线状态，私有 metadata/凭据字段一律剥离；
//   - 快照分帧发送，避免超大房间产生单个巨帧；重连按 revision 决定是否重建快照。
//
// 本包不依赖 repository/service，只依赖 model 与契约包，避免与热点文件形成循环依赖。
package realtime

import (
	"encoding/json"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
)

// 事件类型常量，命名严格对齐契约 5.2。
const (
	EventHello         = "hello"
	EventSnapshotStart = "snapshot-start"
	EventSnapshotNode  = "snapshot-node"
	EventSnapshotEdge  = "snapshot-edge"
	EventSnapshotEnd   = "snapshot-end"
	EventChanges       = "changes"
	EventPresence      = "presence"
	EventCursors       = "cursors"
	EventError         = "error"
)

// 客户端事件类型。
const (
	ClientHello   = "hello"
	ClientCursors = "cursors"
)

// HelloEvent 是服务端连接确认：协议版本、房间、当前身份与 revision。
// 契约里 hello 为 C→S；服务端回发同名 hello 作为连接确认（任务 C01-05 要求）。
type HelloEvent struct {
	Type            string `json:"type"`
	ProtocolVersion string `json:"protocolVersion"`
	RoomID          string `json:"roomId"`
	UserID          string `json:"userId"`
	Role            string `json:"role"`
	Revision        int64  `json:"revision"`
}

// RoomInfo 是快照起始帧里的房间公开信息。
type RoomInfo struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Revision int64  `json:"revision"`
	Role     string `json:"role"`
}

// SnapshotStartEvent 开启一次分帧快照。
type SnapshotStartEvent struct {
	Type   string   `json:"type"`
	UserID string   `json:"userId"`
	Room   RoomInfo `json:"room"`
}

// Position 是画布世界坐标（与契约一致）。
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// NodeView 是广播用的公共图节点视图。Metadata 已剥离 private/local/凭据字段。
type NodeView struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind"`
	Title    string         `json:"title"`
	Position Position       `json:"position"`
	Width    float64        `json:"width"`
	Height   float64        `json:"height"`
	Version  int            `json:"version"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// EdgeView 是广播用的公共图连线视图。
type EdgeView struct {
	ID         string `json:"id"`
	FromNodeID string `json:"fromNodeId"`
	ToNodeID   string `json:"toNodeId"`
	Version    int    `json:"version"`
}

// SnapshotNodeEvent 是快照中的单条节点帧。ownPrivate 表示该私有节点是否属于本人；
// 当前公共图只存公共节点，ownPrivate 恒为 false，他人私有节点不会出现在公共图。
type SnapshotNodeEvent struct {
	Type       string   `json:"type"`
	Node       NodeView `json:"node"`
	OwnPrivate bool     `json:"ownPrivate"`
}

// SnapshotEdgeEvent 是快照中的单条连线帧。
type SnapshotEdgeEvent struct {
	Type string   `json:"type"`
	Edge EdgeView `json:"edge"`
}

// SnapshotEndEvent 结束一次分帧快照，携带结束时的 revision。
type SnapshotEndEvent struct {
	Type     string `json:"type"`
	Revision int64  `json:"revision"`
}

// ChangeView 是一次变更广播中的单项，type 取值 upsert/delete/edge-upsert/edge-delete。
type ChangeView struct {
	Type string    `json:"type"`
	Node *NodeView `json:"node,omitempty"`
	Edge *EdgeView `json:"edge,omitempty"`
}

// ChangesEvent 是提交成功后广播的增量变更。
// HistoryID 由 #11（协作历史审计）补全，本任务留空。
type ChangesEvent struct {
	Type        string       `json:"type"`
	OperationID string       `json:"operationId"`
	HistoryID   string       `json:"historyId"`
	Revision    int64        `json:"revision"`
	Changes     []ChangeView `json:"changes"`
}

// MemberInfo 是在线成员公开信息（不含邮箱、算力等私有明细）。
type MemberInfo struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
}

// PresenceEvent 是房间在线成员列表。
type PresenceEvent struct {
	Type    string       `json:"type"`
	Count   int          `json:"count"`
	Members []MemberInfo `json:"members"`
}

// CursorInfo 是一条协作光标位置。
type CursorInfo struct {
	UserID   string   `json:"userId"`
	Username string   `json:"username"`
	Position Position `json:"position"`
}

// CursorsEvent 是批量合并后的协作光标。
type CursorsEvent struct {
	Type    string       `json:"type"`
	Cursors []CursorInfo `json:"cursors"`
}

// ErrorEvent 是服务端错误通知，结构对齐契约的 {code,message,details}。
type ErrorEvent struct {
	Type    string         `json:"type"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// ClientMessage 是客户端上行帧。hello 用于首帧鉴权，cursors 用于协作光标。
type ClientMessage struct {
	Type            string          `json:"type"`
	ProtocolVersion string          `json:"protocolVersion"`
	RoomID          string          `json:"roomId"`
	Ticket          string          `json:"ticket"`
	Revision        *int64          `json:"revision,omitempty"`
	Position        *Position       `json:"position,omitempty"`
	Raw             json.RawMessage `json:"-"`
}

// NewNodeView 把公共图节点行转换为广播视图，并剥离 private/local 与凭据字段。
func NewNodeView(row model.RoomNode) NodeView {
	return NodeView{
		ID:       row.ID,
		Kind:     row.Kind,
		Title:    row.Title,
		Position: Position{X: row.PositionX, Y: row.PositionY},
		Width:    row.Width,
		Height:   row.Height,
		Version:  row.Version,
		Metadata: sanitizeMetadata(row.Metadata),
	}
}

// NewEdgeView 把公共图连线行转换为广播视图。
func NewEdgeView(row model.RoomEdge) EdgeView {
	return EdgeView{
		ID:         row.ID,
		FromNodeID: row.FromNodeID,
		ToNodeID:   row.ToNodeID,
		Version:    row.Version,
	}
}

// ChangeViews 把一次批次结果里的变更转换为广播视图，节点 metadata 同样脱敏。
func ChangeViews(changes []model.RoomChange) []ChangeView {
	views := make([]ChangeView, 0, len(changes))
	for _, change := range changes {
		view := ChangeView{Type: change.Type}
		if change.Node != nil {
			node := NewNodeView(*change.Node)
			view.Node = &node
		}
		if change.Edge != nil {
			edge := NewEdgeView(*change.Edge)
			view.Edge = &edge
		}
		views = append(views, view)
	}
	return views
}

// sanitizeMetadata 解析节点 metadata JSON，剔除 private/local 字段与凭据类键，
// 只保留 public 字段。解析失败时返回 nil，绝不把原始串直接广播出去。
func sanitizeMetadata(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return nil
	}
	clean := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if contract.IsForbiddenBroadcastKey(key) {
			continue
		}
		if contract.ClassifyMetadataField(key) != contract.ClassPublic {
			continue
		}
		clean[key] = value
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

// MaskPrivateNode 按契约返回他人私有节点的脱敏占位（不含任何私有 metadata）。
func MaskPrivateNode(row model.RoomNode) NodeView {
	return NodeView{
		ID:       row.ID,
		Kind:     "private",
		Title:    "隐私节点",
		Position: Position{X: row.PositionX, Y: row.PositionY},
		Width:    row.Width,
		Height:   row.Height,
		Version:  row.Version,
		Metadata: map[string]any{"content": ""},
	}
}
