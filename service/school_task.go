package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
)

// 学校任务业务层（C03-01 / #17 定义层）：任务构造、幂等提交解析、状态机迁移、
// 上游绑定与 mock 上游契约。不实现持久化、FIFO 单执行槽（#18）与设备执行器（#19）。

// 学校任务业务错误。handler 依据这些哨兵错误映射 HTTP 状态码。
var (
	// ErrSchoolTaskNotFound 表示学校任务不存在或当前用户无权访问。
	ErrSchoolTaskNotFound = errors.New("学校任务不存在")
	// ErrSchoolTaskForbidden 表示当前用户是房间成员但无权提交学校任务（如 viewer）。
	ErrSchoolTaskForbidden = errors.New("无权提交学校任务")
)

// schoolTaskClock 保证同一进程内 school_tasks.created_at 严格单调递增。
// FIFO 领取按 created_at 排序，而任务 ID 是幂等键的哈希、不携带提交顺序，
// 因此同一微秒内的两次提交若 created_at 相同，排序会退化为按哈希 ID 决序而不确定。
// 这里对同微秒或时钟回拨的情况逐微秒前移，保证「后提交的 created_at 更大」。
var schoolTaskClock struct {
	mu   sync.Mutex
	last time.Time
}

func nextSchoolTaskCreatedAt() time.Time {
	schoolTaskClock.mu.Lock()
	defer schoolTaskClock.mu.Unlock()
	now := time.Now().UTC()
	if !now.After(schoolTaskClock.last) {
		now = schoolTaskClock.last.Add(time.Microsecond)
	}
	schoolTaskClock.last = now
	return now
}

// NewSchoolTask 构造一个待提交的 queued 学校任务：派生确定性任务 ID，记录真实
// 提交人，并校验房间、身份与渠道参数。重复提交由 ResolveSchoolTaskIdempotency 处理。
func NewSchoolTask(roomID, submittedBy string, channel model.SchoolChannel, clientTaskID string, params model.SchoolTaskParams) (model.SchoolTask, error) {
	clientTaskID = strings.TrimSpace(clientTaskID)
	if clientTaskID == "" {
		return model.SchoolTask{}, contractFailure(contract.CodeInvalidPayload, "clientTaskId 不能为空", nil)
	}
	roomID = strings.TrimSpace(roomID)
	submittedBy = strings.TrimSpace(submittedBy)
	now := nextSchoolTaskCreatedAt()
	task := model.SchoolTask{
		ID:           model.SchoolTaskID(roomID, submittedBy, clientTaskID),
		RoomID:       roomID,
		SubmittedBy:  submittedBy,
		ClientTaskID: clientTaskID,
		Channel:      channel,
		Status:       model.SchoolTaskQueued,
		Params:       params,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := model.ValidateSchoolTask(task); err != nil {
		return model.SchoolTask{}, err
	}
	return task, nil
}

// RequireSchoolTaskSubmitter 校验成员可提交学校任务：owner/editor 通过，
// viewer 返回 ErrSchoolTaskForbidden（安全矩阵：viewer 不得生成）。
func RequireSchoolTaskSubmitter(member model.RoomMember) error {
	if !member.Role.CanWrite() {
		return ErrSchoolTaskForbidden
	}
	return nil
}

// ResolveSchoolTaskIdempotency 处理重复提交：命中同一幂等任务时返回既有任务，
// 不触发第二次学校执行；同一幂等键但渠道/参数不同返回 operation_id_reused。
// 纯函数，供 #18 接持久化层调用。
func ResolveSchoolTaskIdempotency(existing *model.SchoolTask, incoming model.SchoolTask) (model.SchoolTask, bool, error) {
	if existing == nil {
		return incoming, false, nil
	}
	if existing.ID != incoming.ID {
		return model.SchoolTask{}, false, contractFailure(contract.CodeInvalidPayload, "幂等任务 ID 不一致", nil)
	}
	if existing.Channel != incoming.Channel || !sameSchoolTaskParams(existing.Params, incoming.Params) {
		return model.SchoolTask{}, false, contractFailure(contract.CodeOperationIDReused,
			"clientTaskId 已用于不同参数的学校任务", map[string]any{"taskId": existing.ID})
	}
	return *existing, true, nil
}

// ApplySchoolTaskTransition 按状态机校验并应用一次状态迁移；非法迁移返回
// version_conflict（409）。
func ApplySchoolTaskTransition(task *model.SchoolTask, next model.SchoolTaskStatus) error {
	if task == nil {
		return contractFailure(contract.CodeInvalidPayload, "学校任务不能为空", nil)
	}
	if !next.Valid() {
		return contractFailure(contract.CodeInvalidPayload, fmt.Sprintf("未知任务状态 %q", next), nil)
	}
	if !model.CanTransitionSchoolTask(task.Status, next) {
		return contractFailure(contract.CodeVersionConflict, fmt.Sprintf(
			"学校任务不能从 %s 迁移到 %s", task.Status, next),
			map[string]any{"from": task.Status, "to": next})
	}
	task.Status = next
	task.UpdatedAt = time.Now().UTC()
	return nil
}

// BindSchoolTaskUpstream 绑定上游任务号；首次绑定写入，重复绑定同一号幂等成功，
// 绑定到不同号返回 version_conflict（不可替换）。
func BindSchoolTaskUpstream(task *model.SchoolTask, upstreamTaskID string) error {
	upstreamTaskID = strings.TrimSpace(upstreamTaskID)
	if task == nil {
		return contractFailure(contract.CodeInvalidPayload, "学校任务不能为空", nil)
	}
	if upstreamTaskID == "" {
		return contractFailure(contract.CodeInvalidPayload, "upstreamTaskId 不能为空", nil)
	}
	if task.UpstreamTaskID == "" {
		task.UpstreamTaskID = upstreamTaskID
		task.UpdatedAt = time.Now().UTC()
		return nil
	}
	if task.UpstreamTaskID == upstreamTaskID {
		return nil
	}
	return contractFailure(contract.CodeVersionConflict, "upstreamTaskId 已绑定，不可替换",
		map[string]any{"bound": task.UpstreamTaskID})
}

// sameSchoolTaskParams 比较两个学校任务参数是否一致（幂等判定依据）。
func sameSchoolTaskParams(a, b model.SchoolTaskParams) bool {
	if a.Prompt != b.Prompt || a.VideoSeconds != b.VideoSeconds || a.Size != b.Size || a.Quality != b.Quality {
		return false
	}
	if len(a.ReferenceImages) != len(b.ReferenceImages) {
		return false
	}
	for i := range a.ReferenceImages {
		if a.ReferenceImages[i] != b.ReferenceImages[i] {
			return false
		}
	}
	return true
}

// --- mock 上游契约（执行器 <-> mock 学校服务） ---

// SchoolUpstreamTask 是 mock 学校服务对一次上游任务的状态视图。
type SchoolUpstreamTask struct {
	UpstreamTaskID string                 `json:"upstreamTaskId"`
	Status         model.SchoolTaskStatus `json:"status"`
	Progress       int                    `json:"progress"`
	ResultURLs     []string               `json:"resultUrls,omitempty"`
	Error          string                 `json:"error,omitempty"`
}

// SchoolUpstreamClient 是执行器与 mock 学校服务之间的最小接口：提交、查询、取结果。
// 本期只定义契约，#18/#22 用假数据实现，不接触真实设备或凭据。
type SchoolUpstreamClient interface {
	// Submit 把一个已校验的学校任务提交到上游，返回上游任务号与初始状态。
	Submit(ctx context.Context, task model.SchoolTask) (SchoolUpstreamTask, error)
	// Query 查询上游任务当前状态。
	Query(ctx context.Context, upstreamTaskID string) (SchoolUpstreamTask, error)
	// Result 取回上游任务的成功产物 URL 列表。
	Result(ctx context.Context, upstreamTaskID string) ([]string, error)
}
