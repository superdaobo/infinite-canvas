package model

import "time"

// RoomHistoryStatus 取值。
const (
	// RoomHistoryApplied 表示该历史对应的变更当前仍在房间图中。
	RoomHistoryApplied = "applied"
	// RoomHistoryUndone 表示该历史已被本人撤销，图中处于撤销后的状态。
	RoomHistoryUndone = "undone"
)

// RoomHistory 是协作房间一次成功操作批次（operationId）的审计记录（协作 v1，#11）。
// 一行对应一个批次：记录操作者、时间、房间 revision、人类可读变更摘要与逐目标的
// 变更明细（含操作前后版本与行快照）。它同时是撤销/重做所需的依据：
//   - 审计：谁在何时对哪个房间做了什么，前后版本是多少；
//   - 撤销/重做：按用户隔离，切换每个目标在 Before/After 状态之间，切换时以行版本
//     做条件 CAS，若目标已被他人修改（版本前进）则整条历史失效并返回冲突。
type RoomHistory struct {
	ID          string    `json:"id" gorm:"primaryKey;size:64"`
	RoomID      string    `json:"roomId" gorm:"size:64;index:idx_collab_room_history_room_revision,priority:1"`
	OperationID string    `json:"operationId" gorm:"size:64"`
	ActorID     string    `json:"actorId" gorm:"size:64;index:idx_collab_room_history_actor"`
	Revision    int64     `json:"revision" gorm:"index:idx_collab_room_history_room_revision,priority:2"`
	Summary     string    `json:"summary" gorm:"size:1024"`
	OpsJSON     string    `json:"-" gorm:"column:ops_json;type:text"`
	Status      string    `json:"status" gorm:"size:16"`
	UndoneBy    string    `json:"undoneBy" gorm:"size:64"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`

	// Ops 是 OpsJSON 解析后的明细，仅用于 JSON 响应，不落库。
	Ops []RoomHistoryOp `json:"ops" gorm:"-"`
}

// TableName 使用 collab_ 前缀，避免与其他模块的表名冲突。
func (RoomHistory) TableName() string { return "collab_room_history" }

// RoomHistoryState 是撤销/重做切换用的目标行快照；Node 与 Edge 至多一个非空，
// 指针为 nil 表示该目标在当前状态下“不存在”。
type RoomHistoryState struct {
	Node *RoomNode `json:"node,omitempty"`
	Edge *RoomEdge `json:"edge,omitempty"`
}

// RoomHistoryOp 是批次内单个操作的历史明细。
//
// Before/After 是操作前后的目标行状态：create/connect 的 Before 为 nil（此前不存在），
// delete/disconnect 的 After 为带 Deleted=true 的行（墓碑），二者都是合法切换状态。
// Active 标记当前房间图内处于 After(true) 还是 Before(false)；Version 是当前图内该
// 目标的行版本，撤销/重做时作为 CAS 的期望版本。Merged 记录被合并进本条的后续批次数。
type RoomHistoryOp struct {
	Op      string            `json:"op"`
	Target  string            `json:"target"` // node / edge
	ID      string            `json:"id"`
	Before  *RoomHistoryState `json:"before,omitempty"`
	After   *RoomHistoryState `json:"after,omitempty"`
	Active  bool              `json:"active"`
	Version int               `json:"version"`
	Merged  int               `json:"merged,omitempty"`

	// PositionOnly 仅用于写入时的合并判定（连续拖动），不序列化、不落库。
	PositionOnly bool `json:"-"`
}
