package model

import "time"

// RoomNode 是协作房间公共图中的一个节点行（协作 v1）。
// Metadata 只持久化进入公共图的 JSON 字段；坐标拆成两列便于查询。
type RoomNode struct {
	RoomID    string    `json:"roomId" gorm:"primaryKey;size:64;index:idx_collab_room_nodes_room_deleted,priority:1"`
	ID        string    `json:"id" gorm:"primaryKey;size:64"`
	Kind      string    `json:"kind" gorm:"size:32"`
	Title     string    `json:"title" gorm:"size:512"`
	PositionX float64   `json:"positionX"`
	PositionY float64   `json:"positionY"`
	Width     float64   `json:"width"`
	Height    float64   `json:"height"`
	Version   int       `json:"version"`
	Metadata  string    `json:"metadata" gorm:"type:text"`
	Deleted   bool      `json:"deleted" gorm:"index:idx_collab_room_nodes_room_deleted,priority:2"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName 使用 collab_ 前缀，避免与其他模块的表名冲突。
func (RoomNode) TableName() string { return "collab_room_nodes" }

// RoomEdge 是协作房间公共图中的一条有向连线行（fromNodeId -> toNodeId）。
type RoomEdge struct {
	RoomID     string    `json:"roomId" gorm:"primaryKey;size:64;index:idx_collab_room_edges_room_deleted,priority:1"`
	ID         string    `json:"id" gorm:"primaryKey;size:64"`
	FromNodeID string    `json:"fromNodeId" gorm:"size:64"`
	ToNodeID   string    `json:"toNodeId" gorm:"size:64"`
	Version    int       `json:"version"`
	Deleted    bool      `json:"deleted" gorm:"index:idx_collab_room_edges_room_deleted,priority:2"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// TableName 使用 collab_ 前缀，避免与其他模块的表名冲突。
func (RoomEdge) TableName() string { return "collab_room_edges" }

// RoomOperation 是幂等回执行：记录 (roomId,userId,operationId) 的请求摘要与结果，
// 保证同一 operationId 重试不重复执行、不重复推进 revision。
type RoomOperation struct {
	RoomID        string    `json:"roomId" gorm:"primaryKey;size:64"`
	UserID        string    `json:"userId" gorm:"primaryKey;size:64"`
	OperationID   string    `json:"operationId" gorm:"primaryKey;size:64"`
	RequestDigest string    `json:"requestDigest" gorm:"size:64"`
	Revision      int64     `json:"revision"`
	ResponseJSON  string    `json:"responseJson" gorm:"type:text"`
	CreatedAt     time.Time `json:"createdAt"`
}

// TableName 使用 collab_ 前缀，避免与其他模块的表名冲突。
func (RoomOperation) TableName() string { return "collab_room_operations" }

// RoomChange 是一次批次提交产生的单项变更，对应契约 changes[].type。
type RoomChange struct {
	Type string    `json:"type"` // upsert / delete / edge-upsert / edge-delete
	Node *RoomNode `json:"node,omitempty"`
	Edge *RoomEdge `json:"edge,omitempty"`
}

// RoomBatchResult 是一次原子批量提交的结果，也是幂等重放的返回值。
type RoomBatchResult struct {
	OperationID string       `json:"operationId"`
	Revision    int64        `json:"revision"`
	Changes     []RoomChange `json:"changes"`
	Idempotent  bool         `json:"idempotent"`
}

// RoomGraph 是房间公共图快照。
type RoomGraph struct {
	RoomID   string     `json:"roomId"`
	Revision int64      `json:"revision"`
	Nodes    []RoomNode `json:"nodes"`
	Edges    []RoomEdge `json:"edges"`
}
