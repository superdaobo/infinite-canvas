package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 学校任务队列业务层（C03-02 / #18）：持久化提交、FIFO 单执行槽领取、取消、
// 设备回传与恢复语义。契约层（#17）提供幂等派生、状态机与渠道校验；本层负责
// 落库与事务边界。本期只 mock 上游，不接触真实学校设备/凭据。

// 学校队列专用错误码，补充契约 §8 未覆盖的队列/设备场景。
const (
	// CodeSchoolQueueFull 队列满额（每人排队上限），429。
	CodeSchoolQueueFull = "queue_full"
	// CodeSchoolExecutorUnauthorized 设备身份缺失或授权过期，401。
	CodeSchoolExecutorUnauthorized = "executor_unauthorized"
	// CodeSchoolTaskNotCancellable 仅本人未开始任务可取消，409。
	CodeSchoolTaskNotCancellable = "not_cancellable"
	// CodeSchoolTaskNotRunning 任务不属于该执行器或未在执行，409。
	CodeSchoolTaskNotRunning = "not_running"
)

// ErrSchoolExecutorUnauthorized 表示学校执行设备令牌缺失、无效或已过期。
var ErrSchoolExecutorUnauthorized = errors.New("学校执行器认证失败")

// MaxQueuedSchoolTasksPerUser 是单用户在同一房间的排队上限（默认 20，与 PoC 一致）。
// 声明为变量，便于测试收紧阈值。
var MaxQueuedSchoolTasksPerUser = 20

// SchoolStaleLeaseThreshold 是判定执行设备中断的心跳超时阈值。
var SchoolStaleLeaseThreshold = 2 * time.Minute

// SubmitSchoolTask 提交一个学校任务：校验房间写权限、渠道参数，按
// (roomId, submittedBy, clientTaskId) 幂等。返回 idempotent=true 表示命中既有任务，
// 不产生第二次学校执行。
func SubmitSchoolTask(ctx context.Context, roomID string, channel model.SchoolChannel, clientTaskID string, params model.SchoolTaskParams) (model.SchoolTask, bool, error) {
	member, err := CurrentUserRoomMember(ctx, roomID)
	if err != nil {
		return model.SchoolTask{}, false, err
	}
	if err := RequireSchoolTaskSubmitter(member); err != nil {
		return model.SchoolTask{}, false, err
	}
	incoming, err := NewSchoolTask(roomID, member.UserID, channel, clientTaskID, params)
	if err != nil {
		return model.SchoolTask{}, false, err
	}
	existing, found, err := repository.FindSchoolTaskByIdempotency(roomID, member.UserID, clientTaskID)
	if err != nil {
		return model.SchoolTask{}, false, err
	}
	if found {
		return ResolveSchoolTaskIdempotency(&existing, incoming)
	}
	queued, err := repository.CountQueuedSchoolTasksByUser(roomID, member.UserID)
	if err != nil {
		return model.SchoolTask{}, false, err
	}
	if queued >= int64(MaxQueuedSchoolTasksPerUser) {
		return model.SchoolTask{}, false, contractFailure(CodeSchoolQueueFull,
			"学校任务队列已满，请等待当前任务完成后再提交", map[string]any{"limit": MaxQueuedSchoolTasksPerUser})
	}
	created, err := repository.InsertSchoolTask(incoming)
	if err != nil {
		return model.SchoolTask{}, false, err
	}
	if !created {
		// 并发重复提交：读取胜出的那条并按幂等语义返回。
		existing, found, err := repository.FindSchoolTaskByIdempotency(roomID, member.UserID, clientTaskID)
		if err != nil {
			return model.SchoolTask{}, false, err
		}
		if found {
			return ResolveSchoolTaskIdempotency(&existing, incoming)
		}
		return model.SchoolTask{}, false, contractFailure(contract.CodeVersionConflict, "学校任务并发提交冲突，请重试", nil)
	}
	return incoming, false, nil
}

// ListRoomSchoolTasks 返回房间队列（成员可见），Items 按 FIFO 可见顺序排列。
func ListRoomSchoolTasks(ctx context.Context, roomID string) (model.SchoolTaskQueue, error) {
	if _, err := CurrentUserRoomMember(ctx, roomID); err != nil {
		return model.SchoolTaskQueue{}, err
	}
	tasks, err := repository.ListSchoolTasksByRoom(roomID)
	if err != nil {
		return model.SchoolTaskQueue{}, err
	}
	queue := model.SchoolTaskQueue{Items: make([]model.SchoolTaskPublicView, 0, len(tasks)), Total: int64(len(tasks))}
	for index := range tasks {
		view := tasks[index].PublicView()
		queue.Items = append(queue.Items, view)
		if tasks[index].Status == model.SchoolTaskRunning && queue.Running == nil {
			running := view
			queue.Running = &running
		}
	}
	return queue, nil
}

// CancelSchoolTask 取消本人提交的未开始（queued）任务；他人任务返回 forbidden，
// 已 running/终态任务返回 not_cancellable。
func CancelSchoolTask(ctx context.Context, roomID, taskID string) (model.SchoolTask, error) {
	member, err := CurrentUserRoomMember(ctx, roomID)
	if err != nil {
		return model.SchoolTask{}, err
	}
	task, ok, err := repository.FindSchoolTaskByID(taskID)
	if err != nil {
		return model.SchoolTask{}, err
	}
	if !ok || task.RoomID != strings.TrimSpace(roomID) {
		return model.SchoolTask{}, contractFailure(contract.CodeNotFound, "学校任务不存在", nil)
	}
	if task.SubmittedBy != member.UserID {
		return model.SchoolTask{}, contractFailure(contract.CodeForbidden, "只能取消本人提交的学校任务", nil)
	}
	if task.Status != model.SchoolTaskQueued {
		return model.SchoolTask{}, contractFailure(CodeSchoolTaskNotCancellable,
			"只能取消未开始的学校任务；已提交学校的任务不会被直接取消", map[string]any{"status": task.Status})
	}
	cancelled, err := repository.CancelQueuedSchoolTask(roomID, taskID, member.UserID)
	if err != nil {
		return model.SchoolTask{}, err
	}
	if !cancelled {
		return model.SchoolTask{}, contractFailure(CodeSchoolTaskNotCancellable, "学校任务已被领取或取消，请刷新后重试", nil)
	}
	task.Status = model.SchoolTaskCancelled
	task.UpdatedAt = time.Now().UTC()
	return task, nil
}

// ClaimSchoolTask 设备领取下一个 queued 任务（FIFO 单执行槽）。Blocked=true 表示
// 单执行槽被一个 running 任务占用；此时设备应等待或走恢复流程，不得重复提交。
// 保留按 executorID 的旧入口，不绑定学校账号（兼容 #18 调用方）。
func ClaimSchoolTask(ctx context.Context, executorID string) (model.SchoolTaskClaim, error) {
	return ClaimSchoolTaskForDevice(ctx, SchoolExecutor{ID: executorID})
}

// ClaimSchoolTaskForDevice 受限设备领取任务：把设备当前的学校账号指纹绑定写入任务，
// 后续重新提交（绑定上游/恢复）都要求账号一致（C03-03 / #19）。
func ClaimSchoolTaskForDevice(ctx context.Context, executor SchoolExecutor) (model.SchoolTaskClaim, error) {
	executorID := strings.TrimSpace(executor.ID)
	if executorID == "" {
		return model.SchoolTaskClaim{}, contractFailure(CodeSchoolExecutorUnauthorized, "执行设备身份无效", nil)
	}
	task, ok, err := repository.ClaimNextQueuedSchoolTaskForAccount(executorID, strings.TrimSpace(executor.SchoolAccountRef))
	if err != nil {
		return model.SchoolTaskClaim{}, err
	}
	if ok {
		view := task.DeviceView()
		return model.SchoolTaskClaim{Task: &view}, nil
	}
	claim := model.SchoolTaskClaim{}
	if running, found, err := repository.FindRunningSchoolTask(); err != nil {
		return model.SchoolTaskClaim{}, err
	} else if found {
		claim.Blocked = true
		claim.RunningTaskID = running.ID
	}
	return claim, nil
}

// BindSchoolTaskUpstreamForExecutor 设备回传上游任务号：首次绑定写入，重复绑定同一号
// 幂等成功，绑定到不同号返回 version_conflict。同时刷新心跳。
func BindSchoolTaskUpstreamForExecutor(ctx context.Context, executorID, taskID, upstreamTaskID string) (model.SchoolTask, error) {
	task, err := requireRunningSchoolTask(executorID, taskID)
	if err != nil {
		return model.SchoolTask{}, err
	}
	return bindSchoolTaskUpstream(executorID, task, upstreamTaskID)
}

// BindSchoolTaskUpstreamForDevice 在回传上游任务号（即向学校重新提交）前校验学校账号绑定：
// 设备当前账号与任务绑定账号不一致时返回 school_account_changed，任务保持 running、
// 单执行槽不释放、绝不重复提交。
func BindSchoolTaskUpstreamForDevice(ctx context.Context, executor SchoolExecutor, taskID, upstreamTaskID string) (model.SchoolTask, error) {
	task, err := requireRunningSchoolTask(executor.ID, taskID)
	if err != nil {
		return model.SchoolTask{}, err
	}
	if model.SchoolAccountMismatch(task.SchoolAccountRef, executor.SchoolAccountRef) {
		return model.SchoolTask{}, contractFailure(CodeSchoolAccountChanged,
			"学校账号已变更，旧任务不能在新账号下重新提交", map[string]any{"taskId": task.ID})
	}
	return bindSchoolTaskUpstream(executor.ID, task, upstreamTaskID)
}

// bindSchoolTaskUpstream 是绑定上游任务号的共用实现：首次写入、重复同号幂等、异号冲突。
func bindSchoolTaskUpstream(executorID string, task model.SchoolTask, upstreamTaskID string) (model.SchoolTask, error) {
	if err := BindSchoolTaskUpstream(&task, upstreamTaskID); err != nil {
		return model.SchoolTask{}, err
	}
	if _, err := repository.SetSchoolTaskUpstream(task.ID, task.UpstreamTaskID); err != nil {
		return model.SchoolTask{}, err
	}
	_, _ = repository.TouchRunningSchoolTask(task.ID, executorID)
	return task, nil
}

// FinishSchoolTaskForExecutor 设备结束自己持有的 running 任务：成功释放执行槽，
// 失败记录原因；已结束的任务重复上报按幂等成功返回。
func FinishSchoolTaskForExecutor(ctx context.Context, executorID, taskID string, status model.SchoolTaskStatus, errMsg string) (model.SchoolTask, error) {
	if status != model.SchoolTaskSucceeded && status != model.SchoolTaskFailed {
		return model.SchoolTask{}, contractFailure(contract.CodeInvalidPayload, "任务结束状态只能是 succeeded 或 failed", nil)
	}
	task, ok, err := repository.FindSchoolTaskByID(taskID)
	if err != nil {
		return model.SchoolTask{}, err
	}
	if !ok {
		return model.SchoolTask{}, contractFailure(contract.CodeNotFound, "学校任务不存在", nil)
	}
	if task.Status.Terminal() {
		return task, nil // 幂等：已结束任务重复上报
	}
	if task.Status != model.SchoolTaskRunning || task.ExecutorID != strings.TrimSpace(executorID) {
		return model.SchoolTask{}, contractFailure(CodeSchoolTaskNotRunning, "任务不属于该执行器或未在执行", nil)
	}
	if err := ApplySchoolTaskTransition(&task, status); err != nil {
		return model.SchoolTask{}, err
	}
	// 失败信息先脱敏再落库：绝不把底层凭据写入任务记录或对外视图（#22 安全边界）。
	changed, err := repository.FinishRunningSchoolTask(taskID, executorID, status, sanitizeSchoolFailure(errMsg))
	if err != nil {
		return model.SchoolTask{}, err
	}
	if !changed {
		return model.SchoolTask{}, contractFailure(CodeSchoolTaskNotRunning, "任务已被结束或不属于该执行器", nil)
	}
	return task, nil
}

// RecoverRunningSchoolTask 把 running 任务重新绑定到当前执行设备，保留原
// upstream_task_id 与 running 状态（服务重启/设备中断后不重复生成）。note 作为
// 可审计说明写入任务记录。保留按 executorID 的旧入口，不校验学校账号绑定。
func RecoverRunningSchoolTask(ctx context.Context, executorID, taskID, note string, confirmPreviousStopped bool) (model.SchoolTask, error) {
	return recoverRunningSchoolTask(executorID, "", taskID, note, confirmPreviousStopped)
}

// RecoverRunningSchoolTaskForDevice 与 RecoverRunningSchoolTask 相同，但额外校验
// 学校账号绑定：设备当前账号与任务绑定账号不一致时拒绝恢复（school_account_changed），
// 任务保持 running、单执行槽不释放、绝不重复提交（C03-03 / #19）。
func RecoverRunningSchoolTaskForDevice(ctx context.Context, executor SchoolExecutor, taskID, note string, confirmPreviousStopped bool) (model.SchoolTask, error) {
	return recoverRunningSchoolTask(executor.ID, executor.SchoolAccountRef, taskID, note, confirmPreviousStopped)
}

func recoverRunningSchoolTask(executorID, accountRef, taskID, note string, confirmPreviousStopped bool) (model.SchoolTask, error) {
	executorID = strings.TrimSpace(executorID)
	if executorID == "" {
		return model.SchoolTask{}, contractFailure(CodeSchoolExecutorUnauthorized, "执行设备身份无效", nil)
	}
	if !confirmPreviousStopped {
		return model.SchoolTask{}, contractFailure(contract.CodeInvalidPayload, "恢复前必须确认旧执行设备已停止", nil)
	}
	task, ok, err := repository.FindSchoolTaskByID(taskID)
	if err != nil {
		return model.SchoolTask{}, err
	}
	if !ok {
		return model.SchoolTask{}, contractFailure(contract.CodeNotFound, "学校任务不存在", nil)
	}
	if task.Status != model.SchoolTaskRunning {
		return model.SchoolTask{}, contractFailure(CodeSchoolTaskNotRunning, "没有可恢复的执行中任务", map[string]any{"status": task.Status})
	}
	if model.SchoolAccountMismatch(task.SchoolAccountRef, accountRef) {
		return model.SchoolTask{}, contractFailure(CodeSchoolAccountChanged,
			"学校账号已变更，不能在新账号下恢复旧任务", map[string]any{"taskId": task.ID})
	}
	if strings.TrimSpace(note) == "" {
		note = "执行器已重新绑定，等待恢复原学校任务"
	}
	changed, err := repository.RebindRunningSchoolTaskExecutor(taskID, executorID, note)
	if err != nil {
		return model.SchoolTask{}, err
	}
	if !changed {
		return model.SchoolTask{}, contractFailure(CodeSchoolTaskNotRunning, "恢复失败，任务状态已变化", nil)
	}
	task.ExecutorID = executorID
	task.Error = note
	task.UpdatedAt = time.Now().UTC()
	return task, nil
}

// SchoolQueueRecovery 是启动恢复的只读统计：运行中任务数与其中判定为中断（stale）的数量。
type SchoolQueueRecovery struct {
	Running int `json:"running"`
	Stale   int `json:"stale"`
}

// PrepareSchoolQueueOnStartup 执行服务启动恢复：**只统计，不改写 running 状态**。
// 恢复语义要求重启/中断后 running 保持 running、保留 upstream_task_id 与设备绑定，
// 绝不自动改回 queued，也绝不重复生成。
func PrepareSchoolQueueOnStartup(ctx context.Context) (SchoolQueueRecovery, error) {
	running, err := repository.ListRunningSchoolTasks()
	if err != nil {
		return SchoolQueueRecovery{}, err
	}
	stale, err := repository.CountStaleRunningSchoolTasks(time.Now().UTC().Add(-SchoolStaleLeaseThreshold))
	if err != nil {
		return SchoolQueueRecovery{}, err
	}
	return SchoolQueueRecovery{Running: len(running), Stale: int(stale)}, nil
}

// requireRunningSchoolTask 读取任务并校验它正被指定执行设备持有。
func requireRunningSchoolTask(executorID, taskID string) (model.SchoolTask, error) {
	executorID = strings.TrimSpace(executorID)
	if executorID == "" {
		return model.SchoolTask{}, contractFailure(CodeSchoolExecutorUnauthorized, "执行设备身份无效", nil)
	}
	task, ok, err := repository.FindSchoolTaskByID(taskID)
	if err != nil {
		return model.SchoolTask{}, err
	}
	if !ok {
		return model.SchoolTask{}, contractFailure(contract.CodeNotFound, "学校任务不存在", nil)
	}
	if task.Status != model.SchoolTaskRunning || task.ExecutorID != executorID {
		return model.SchoolTask{}, contractFailure(CodeSchoolTaskNotRunning, "任务不属于该执行器或未在执行", nil)
	}
	return task, nil
}
