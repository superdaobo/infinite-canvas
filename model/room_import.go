package model

// 本文件定义旧单人画布（整份 JSON）安全导入协作房间的输入与报告结构（C04-02 / #25）。
//
// 旧项目是 web 端 CanvasProject 的公共部分：nodes 为 8 类节点，connections 为
// fromNodeId -> toNodeId 的有向连线。导入只把公共字段映射进房间公共图，private/local
// 字段不导入为公共字段；报告绝不回显本地绝对路径或密钥。

// RoomImportRequest 是一次导入请求：mode 为空默认 preview（只读预扫描），
// 只有显式 mode=initial 才会写入空房间。
type RoomImportRequest struct {
	Mode     string             `json:"mode"`
	Snapshot RoomImportSnapshot `json:"snapshot"`
}

// RoomImportSnapshot 是旧画布的整份 JSON 快照。id/title 仅用于溯源与幂等键，
// 不写入房间图。
type RoomImportSnapshot struct {
	ID          string           `json:"id"`
	Title       string           `json:"title"`
	Nodes       []RoomImportNode `json:"nodes"`
	Connections []RoomImportEdge `json:"connections"`
}

// RoomImportNode 是旧画布节点：type 对应 8 类节点（image/panorama/video/audio/text/group/config/director）。
type RoomImportNode struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Position RoomImportPos  `json:"position"`
	Width    float64        `json:"width"`
	Height   float64        `json:"height"`
	Metadata map[string]any `json:"metadata"`
}

// RoomImportPos 是旧画布节点坐标。
type RoomImportPos struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// RoomImportEdge 是旧画布连线（有向）。
type RoomImportEdge struct {
	ID         string `json:"id"`
	FromNodeID string `json:"fromNodeId"`
	ToNodeID   string `json:"toNodeId"`
}

// RoomImportNodeSummary 是节点预扫描/导入摘要（含 8 类节点计数与字段裁剪统计）。
type RoomImportNodeSummary struct {
	Total                int            `json:"total"`
	Imported             int            `json:"imported"`
	Skipped              int            `json:"skipped"`
	ByKind               map[string]int `json:"byKind"`
	PrivateFieldsDropped int            `json:"privateFieldsDropped"`
	LocalFieldsDropped   int            `json:"localFieldsDropped"`
}

// RoomImportEdgeSummary 是连线预扫描/导入摘要。
type RoomImportEdgeSummary struct {
	Total    int `json:"total"`
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
}

// RoomImportMediaSummary 是媒体迁移摘要。
// Referenced 为带媒体引用的媒体节点数；Migrated 为成功迁移数；Rejected 为拉取成功但
// 类型被白名单拒绝（SVG/主动内容等）的数量；Missing = Referenced - Migrated（含
// 不可达、非白名单、被拒与拉取失败），即待补传数量。
type RoomImportMediaSummary struct {
	Referenced int `json:"referenced"`
	Migrated   int `json:"migrated"`
	Missing    int `json:"missing"`
	Rejected   int `json:"rejected"`
}

// RoomImportSkippedNode 是被跳过的非法节点及原因（reason 为契约错误码或稳定分类码）。
// Message 为通用说明，绝不回显原始字段名/值，避免泄漏凭据或本地路径。
type RoomImportSkippedNode struct {
	ID      string `json:"id,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}

// RoomImportMissingMedia 是不可迁移/待补传的媒体项，只暴露来源 origin（scheme://host），
// 绝不包含本地绝对路径、查询串或凭据。
type RoomImportMissingMedia struct {
	NodeID string `json:"nodeId"`
	Origin string `json:"origin,omitempty"`
	Reason string `json:"reason"`
}

// RoomImportReport 是导入/预扫描结果报告，也是幂等重放的返回值。
type RoomImportReport struct {
	Mode         string                   `json:"mode"`
	RoomID       string                   `json:"roomId"`
	SourceID     string                   `json:"sourceId,omitempty"`
	Applied      bool                     `json:"applied"`
	Idempotent   bool                     `json:"idempotent"`
	Revision     int64                    `json:"revision"`
	Nodes        RoomImportNodeSummary    `json:"nodes"`
	Edges        RoomImportEdgeSummary    `json:"edges"`
	Media        RoomImportMediaSummary   `json:"media"`
	SkippedNodes []RoomImportSkippedNode  `json:"skippedNodes"`
	SkippedEdges []RoomImportSkippedNode  `json:"skippedEdges"`
	MissingMedia []RoomImportMissingMedia `json:"missingMedia"`
	Warnings     []string                 `json:"warnings,omitempty"`
}
