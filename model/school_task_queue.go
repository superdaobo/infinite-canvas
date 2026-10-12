package model

import (
	"encoding/json"
	"time"
)

// 本文件落地学校任务的持久化表模型（C03-02 / #18）。
//
// 契约层 model.SchoolTask 保持无 gorm 标签；本文件用 SchoolTaskRecord 承载
// school_tasks 表，并负责与领域模型互转。队列的两个硬约束都在数据库层：
//   1. 幂等唯一键 (room_id, submitted_by, client_task_id)；
//   2. running_slot 唯一索引：只有 running 任务写入常量槽位，其余为 NULL，
//      借助 SQLite/MySQL/PostgreSQL「唯一索引不把多个 NULL 视为重复」的语义，
//      保证全局最多只有一个 running 任务（单执行槽）。

// SchoolTaskRunningSlot 是 running 任务的槽位标记值；非 running 状态必须为 NULL。
const SchoolTaskRunningSlot = 1

// SchoolTaskRecord 是 school_tasks 持久化行。
type SchoolTaskRecord struct {
	ID           string `gorm:"primaryKey;size:64"`
	RoomID       string `gorm:"size:64;uniqueIndex:idx_school_tasks_idem,priority:1"`
	SubmittedBy  string `gorm:"size:64;uniqueIndex:idx_school_tasks_idem,priority:2"`
	ClientTaskID string `gorm:"size:128;uniqueIndex:idx_school_tasks_idem,priority:3"`
	// 受限设备身份；未领取时为空。与用户 JWT 完全分离。
	ExecutorID string `gorm:"size:64"`
	// 上游学校任务号；一旦绑定不可替换。
	UpstreamTaskID string `gorm:"size:128"`
	Channel        string `gorm:"size:16"`
	Status         string `gorm:"size:16;index:idx_school_tasks_queue,priority:1"`
	ParamsJSON     string `gorm:"type:text"`
	Error          string `gorm:"type:text"`
	// RunningSlot 仅在任务处于 running 时为非 NULL（值为 SchoolTaskRunningSlot）。
	// 唯一索引由此保证全局单执行槽。
	RunningSlot *int       `gorm:"uniqueIndex:idx_school_tasks_one_running"`
	LastSeenAt  *time.Time `gorm:"precision:6"` // 设备心跳时间，用于识别中断
	CreatedAt   time.Time  `gorm:"precision:6;index:idx_school_tasks_queue,priority:2"`
	UpdatedAt   time.Time  `gorm:"precision:6"`
}

// TableName 使用 school_ 前缀，与协作房间表区分。
func (SchoolTaskRecord) TableName() string { return "school_tasks" }

// NewSchoolTaskRecord 把领域任务转换为持久化行，并按状态维护 running 槽位。
func NewSchoolTaskRecord(task SchoolTask) SchoolTaskRecord {
	paramsJSON, _ := json.Marshal(task.Params)
	record := SchoolTaskRecord{
		ID:             task.ID,
		RoomID:         task.RoomID,
		SubmittedBy:    task.SubmittedBy,
		ClientTaskID:   task.ClientTaskID,
		ExecutorID:     task.ExecutorID,
		UpstreamTaskID: task.UpstreamTaskID,
		Channel:        string(task.Channel),
		Status:         string(task.Status),
		ParamsJSON:     string(paramsJSON),
		Error:          task.Error,
		LastSeenAt:     task.LastSeenAt,
		CreatedAt:      task.CreatedAt,
		UpdatedAt:      task.UpdatedAt,
	}
	if task.Status == SchoolTaskRunning {
		slot := SchoolTaskRunningSlot
		record.RunningSlot = &slot
	}
	return record
}

// ToSchoolTask 把持久化行还原为领域任务。
func (r SchoolTaskRecord) ToSchoolTask() SchoolTask {
	var params SchoolTaskParams
	if r.ParamsJSON != "" {
		_ = json.Unmarshal([]byte(r.ParamsJSON), &params)
	}
	return SchoolTask{
		ID:             r.ID,
		RoomID:         r.RoomID,
		SubmittedBy:    r.SubmittedBy,
		ExecutorID:     r.ExecutorID,
		ClientTaskID:   r.ClientTaskID,
		UpstreamTaskID: r.UpstreamTaskID,
		Channel:        SchoolChannel(r.Channel),
		Status:         SchoolTaskStatus(r.Status),
		Params:         params,
		Error:          r.Error,
		LastSeenAt:     r.LastSeenAt,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

// SchoolTaskQueue 是房间学校任务队列视图，Items 按提交顺序（FIFO 可见顺序）排列。
type SchoolTaskQueue struct {
	Items   []SchoolTaskPublicView `json:"items"`
	Total   int64                  `json:"total"`
	Running *SchoolTaskPublicView  `json:"running,omitempty"`
}

// SchoolTaskDeviceView 是下发给受限执行设备的最小任务载荷：只含完成该任务所需
// 的渠道、参数与上游任务号，不含用户凭据、学校 Cookie 或模型密钥。
type SchoolTaskDeviceView struct {
	ID             string           `json:"id"`
	RoomID         string           `json:"roomId"`
	Channel        SchoolChannel    `json:"channel"`
	Params         SchoolTaskParams `json:"params"`
	Status         SchoolTaskStatus `json:"status"`
	ExecutorID     string           `json:"executorId,omitempty"`
	UpstreamTaskID string           `json:"upstreamTaskId,omitempty"`
}

// DeviceView 返回设备侧最小任务载荷。
func (t SchoolTask) DeviceView() SchoolTaskDeviceView {
	return SchoolTaskDeviceView{
		ID:             t.ID,
		RoomID:         t.RoomID,
		Channel:        t.Channel,
		Params:         t.Params,
		Status:         t.Status,
		ExecutorID:     t.ExecutorID,
		UpstreamTaskID: t.UpstreamTaskID,
	}
}

// SchoolTaskClaim 是设备领取任务的结果：Task 为空表示当前无可领取任务；
// Blocked 为 true 表示队列被一个 running 任务占用（单执行槽已满）。
type SchoolTaskClaim struct {
	Task          *SchoolTaskDeviceView `json:"task"`
	Blocked       bool                  `json:"blocked"`
	RunningTaskID string                `json:"runningTaskId,omitempty"`
}
