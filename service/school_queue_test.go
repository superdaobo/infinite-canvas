package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 学校队列契约测试（C03-02 / #18）：FIFO 单执行槽、幂等、取消、恢复与错误语义。
// 所有用例只走 mock 上游与本地内存库，不接触真实学校设备/凭据。

// schoolTestEnv 是一次学校队列测试的房间与两个成员账号。
type schoolTestEnv struct {
	roomID string
	userA  string
	userB  string
	ctxA   context.Context
	ctxB   context.Context
}

// resetSchoolTasks 清空 school_tasks：repository.DB() 是进程级单例，
// 同包多个测试共享同一个 :memory: 库，需在用例开始时重置队列表。
func resetSchoolTasks(t *testing.T) {
	t.Helper()
	db, err := repository.DB()
	if err != nil {
		t.Fatalf("获取测试库失败: %v", err)
	}
	if err := db.Exec("DELETE FROM school_tasks").Error; err != nil {
		t.Fatalf("清空 school_tasks 失败: %v", err)
	}
}

func setupSchoolTestEnv(t *testing.T) *schoolTestEnv {
	t.Helper()
	setupCollabDB(t, "sqlite", ":memory:")
	resetSchoolTasks(t)
	userA := newID("school-user-a")
	userB := newID("school-user-b")
	ctxA := collabContext(userA)
	ctxB := collabContext(userB)
	room, err := CreateRoom(ctxA, "学校队列测试房间")
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	now := time.Now().UTC()
	if err := repository.SaveRoomMember(model.RoomMember{
		RoomID: room.ID, UserID: userB, Role: model.RoomRoleEditor, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入 editor 成员失败: %v", err)
	}
	return &schoolTestEnv{roomID: room.ID, userA: userA, userB: userB, ctxA: ctxA, ctxB: ctxB}
}

func schoolErrorCode(t *testing.T, err error) string {
	t.Helper()
	var contractErr *contract.Error
	if !errors.As(err, &contractErr) {
		t.Fatalf("期望契约错误，实际 %v", err)
	}
	return contractErr.Code
}

func mustSubmit(t *testing.T, ctx context.Context, env *schoolTestEnv, channel model.SchoolChannel, clientTaskID string, params model.SchoolTaskParams) model.SchoolTask {
	t.Helper()
	task, _, err := SubmitSchoolTask(ctx, env.roomID, channel, clientTaskID, params)
	if err != nil {
		t.Fatalf("提交学校任务失败: %v", err)
	}
	return task
}

func mustClaim(t *testing.T, executorID string) (model.SchoolTask, bool) {
	t.Helper()
	claim, err := ClaimSchoolTask(context.Background(), executorID)
	if err != nil {
		t.Fatalf("领取学校任务失败: %v", err)
	}
	if claim.Task == nil {
		return model.SchoolTask{}, false
	}
	task, ok, err := repository.FindSchoolTaskByID(claim.Task.ID)
	if err != nil || !ok {
		t.Fatalf("读取已领取任务失败: ok=%v err=%v", ok, err)
	}
	return task, true
}

// TestSchoolTaskQueueFIFOAndSingleRunning 验证两账号连续提交严格 FIFO，
// 任意时刻只有一个 running lease，结束后才领取下一个。
func TestSchoolTaskQueueFIFOAndSingleRunning(t *testing.T) {
	env := setupSchoolTestEnv(t)
	taskA := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-a1", model.SchoolTaskParams{Prompt: "A 的图"})
	taskB := mustSubmit(t, env.ctxB, env, model.SchoolChannelQwen, "client-b1", model.SchoolTaskParams{Prompt: "B 的图"})

	queue, err := ListRoomSchoolTasks(env.ctxA, env.roomID)
	if err != nil {
		t.Fatalf("读取队列失败: %v", err)
	}
	if queue.Total != 2 || queue.Items[0].ID != taskA.ID || queue.Items[1].ID != taskB.ID {
		t.Fatalf("队列应按提交顺序 FIFO 排列，实际 %+v", queue.Items)
	}

	// 第一个 running lease 必须是先提交的 A。
	runningA, ok := mustClaim(t, "executor-1")
	if !ok || runningA.ID != taskA.ID || runningA.Status != model.SchoolTaskRunning {
		t.Fatalf("首个 running lease 应为 A，实际 ok=%v task=%+v", ok, runningA)
	}
	if runningA.ExecutorID != "executor-1" {
		t.Fatalf("执行设备绑定错误: %s", runningA.ExecutorID)
	}

	// 单执行槽被占用时，任何设备都领不到第二个任务。
	claim, err := ClaimSchoolTask(context.Background(), "executor-2")
	if err != nil {
		t.Fatalf("第二次领取失败: %v", err)
	}
	if claim.Task != nil || !claim.Blocked || claim.RunningTaskID != taskA.ID {
		t.Fatalf("running 占用时不应再领取，实际 %+v", claim)
	}

	// A 结束后，才轮到 B。
	if _, err := FinishSchoolTaskForExecutor(context.Background(), "executor-1", taskA.ID, model.SchoolTaskSucceeded, ""); err != nil {
		t.Fatalf("结束 A 失败: %v", err)
	}
	runningB, ok := mustClaim(t, "executor-2")
	if !ok || runningB.ID != taskB.ID {
		t.Fatalf("A 结束后应领取 B，实际 ok=%v task=%+v", ok, runningB)
	}
	if _, err := FinishSchoolTaskForExecutor(context.Background(), "executor-2", taskB.ID, model.SchoolTaskSucceeded, ""); err != nil {
		t.Fatalf("结束 B 失败: %v", err)
	}
	claim, err = ClaimSchoolTask(context.Background(), "executor-3")
	if err != nil {
		t.Fatalf("队列空时领取失败: %v", err)
	}
	if claim.Task != nil || claim.Blocked {
		t.Fatalf("队列空时应返回空且非 blocked，实际 %+v", claim)
	}
}

// TestSchoolTaskIdempotentSubmit 验证幂等：同 clientTaskId 重复提交返回同一任务、
// 不产生第二次执行；参数不同则 operation_id_reused。
func TestSchoolTaskIdempotentSubmit(t *testing.T) {
	env := setupSchoolTestEnv(t)
	params := model.SchoolTaskParams{Prompt: "同一提示词"}
	first, idempotent, err := SubmitSchoolTask(env.ctxA, env.roomID, model.SchoolChannelQwen, "client-idem", params)
	if err != nil || idempotent {
		t.Fatalf("首次提交应成功且非幂等: idem=%v err=%v", idempotent, err)
	}
	second, idempotent, err := SubmitSchoolTask(env.ctxA, env.roomID, model.SchoolChannelQwen, "client-idem", params)
	if err != nil {
		t.Fatalf("重复提交失败: %v", err)
	}
	if !idempotent || second.ID != first.ID {
		t.Fatalf("重复提交应命中同一任务并标记幂等: idem=%v id=%s want=%s", idempotent, second.ID, first.ID)
	}
	queue, err := ListRoomSchoolTasks(env.ctxA, env.roomID)
	if err != nil {
		t.Fatalf("读取队列失败: %v", err)
	}
	if queue.Total != 1 {
		t.Fatalf("幂等重复提交不应新增任务，实际 total=%d", queue.Total)
	}
	if _, _, err := SubmitSchoolTask(env.ctxA, env.roomID, model.SchoolChannelQwen, "client-idem",
		model.SchoolTaskParams{Prompt: "改过的提示词"}); schoolErrorCode(t, err) != contract.CodeOperationIDReused {
		t.Fatalf("同 clientTaskId 不同参数应 operation_id_reused，实际 %v", err)
	}
}

// TestSchoolTaskCancelRules 验证取消仅限本人未开始任务：他人任务被拒、running 被拒。
func TestSchoolTaskCancelRules(t *testing.T) {
	env := setupSchoolTestEnv(t)
	taskA := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-cancel-a", model.SchoolTaskParams{Prompt: "A"})
	taskB := mustSubmit(t, env.ctxB, env, model.SchoolChannelQwen, "client-cancel-b", model.SchoolTaskParams{Prompt: "B"})

	// 反例：A 取消 B 的任务 → forbidden。
	if _, err := CancelSchoolTask(env.ctxA, env.roomID, taskB.ID); schoolErrorCode(t, err) != contract.CodeForbidden {
		t.Fatalf("取消他人任务应 forbidden，实际 %v", err)
	}
	// 正例：A 取消自己的 queued 任务成功。
	cancelled, err := CancelSchoolTask(env.ctxA, env.roomID, taskA.ID)
	if err != nil || cancelled.Status != model.SchoolTaskCancelled {
		t.Fatalf("取消本人未开始任务应成功，实际 %+v err=%v", cancelled, err)
	}
	// 反例：重复取消同一已取消任务 → not_cancellable。
	if _, err := CancelSchoolTask(env.ctxA, env.roomID, taskA.ID); schoolErrorCode(t, err) != CodeSchoolTaskNotCancellable {
		t.Fatalf("重复取消应 not_cancellable，实际 %v", err)
	}

	// B 的任务被领取后，本人也不能取消（已提交学校，不直接取消）。
	if _, ok := mustClaim(t, "executor-cancel"); !ok {
		t.Fatal("应能领取 B 的任务")
	}
	if _, err := CancelSchoolTask(env.ctxB, env.roomID, taskB.ID); schoolErrorCode(t, err) != CodeSchoolTaskNotCancellable {
		t.Fatalf("取消 running 任务应 not_cancellable，实际 %v", err)
	}

	// 反例：非成员取消 → not_found（不泄漏房间/任务是否存在）。
	outsider := collabContext(newID("school-outsider"))
	if _, err := CancelSchoolTask(outsider, env.roomID, taskB.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("非成员取消应 ErrRoomNotFound，实际 %v", err)
	}
}

// TestSchoolTaskRestartRecovery 验证服务重启/设备中断后 running 保持、不重复生成，
// 且显式恢复可重绑执行设备并保留原上游任务号。
func TestSchoolTaskRestartRecovery(t *testing.T) {
	env := setupSchoolTestEnv(t)
	task := mustSubmit(t, env.ctxA, env, model.SchoolChannelH3, "client-recover",
		model.SchoolTaskParams{Prompt: "参考图视频", ReferenceImages: []string{"img-1", "img-2"}, VideoSeconds: 5})

	running, ok := mustClaim(t, "executor-old")
	if !ok || running.ID != task.ID {
		t.Fatalf("应领取到任务: ok=%v %+v", ok, running)
	}
	bound, err := BindSchoolTaskUpstreamForExecutor(context.Background(), "executor-old", task.ID, "mock-upstream-1")
	if err != nil || bound.UpstreamTaskID != "mock-upstream-1" {
		t.Fatalf("绑定上游任务失败: %+v err=%v", bound, err)
	}
	// 幂等：重复绑定同一上游号成功；绑定不同号被拒。
	if _, err := BindSchoolTaskUpstreamForExecutor(context.Background(), "executor-old", task.ID, "mock-upstream-1"); err != nil {
		t.Fatalf("重复绑定同一上游号应幂等成功: %v", err)
	}
	if _, err := BindSchoolTaskUpstreamForExecutor(context.Background(), "executor-old", task.ID, "mock-upstream-2"); schoolErrorCode(t, err) != contract.CodeVersionConflict {
		t.Fatalf("绑定不同上游号应 version_conflict，实际 %v", err)
	}

	// 模拟服务重启：启动恢复只统计，不改写 running。
	recovery, err := PrepareSchoolQueueOnStartup(context.Background())
	if err != nil {
		t.Fatalf("启动恢复失败: %v", err)
	}
	if recovery.Running != 1 {
		t.Fatalf("重启后应仍有 1 个 running，实际 %d", recovery.Running)
	}
	afterRestart, ok, err := repository.FindSchoolTaskByID(task.ID)
	if err != nil || !ok {
		t.Fatalf("重启后任务应仍存在: ok=%v err=%v", ok, err)
	}
	if afterRestart.Status != model.SchoolTaskRunning || afterRestart.ExecutorID != "executor-old" || afterRestart.UpstreamTaskID != "mock-upstream-1" {
		t.Fatalf("重启不得改写 running/执行设备/上游任务号，实际 %+v", afterRestart)
	}

	// 设备中断：其他设备领取被 blocked，绝不重复生成。
	claim, err := ClaimSchoolTask(context.Background(), "executor-new")
	if err != nil {
		t.Fatalf("中断后领取失败: %v", err)
	}
	if claim.Task != nil || !claim.Blocked || claim.RunningTaskID != task.ID {
		t.Fatalf("中断后不应重复生成，实际 %+v", claim)
	}

	// 恢复需显式确认；未确认被拒。
	if _, err := RecoverRunningSchoolTask(context.Background(), "executor-new", task.ID, "", false); schoolErrorCode(t, err) != contract.CodeInvalidPayload {
		t.Fatalf("未确认旧设备停止应 invalid_payload，实际 %v", err)
	}
	recovered, err := RecoverRunningSchoolTask(context.Background(), "executor-new", task.ID, "", true)
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if recovered.ExecutorID != "executor-new" || recovered.UpstreamTaskID != "mock-upstream-1" || recovered.Status != model.SchoolTaskRunning {
		t.Fatalf("恢复应重绑设备并保留上游任务号，实际 %+v", recovered)
	}

	// 恢复后由新设备结束，释放执行槽。
	if _, err := FinishSchoolTaskForExecutor(context.Background(), "executor-new", task.ID, model.SchoolTaskSucceeded, ""); err != nil {
		t.Fatalf("新设备结束任务失败: %v", err)
	}
	recovery, err = PrepareSchoolQueueOnStartup(context.Background())
	if err != nil || recovery.Running != 0 {
		t.Fatalf("任务结束后不应再有 running，实际 %+v err=%v", recovery, err)
	}
}

// TestSchoolTaskQueueFullAndInvalid 验证队列满额、非法任务、越权都有明确错误码。
func TestSchoolTaskQueueFullAndInvalid(t *testing.T) {
	env := setupSchoolTestEnv(t)

	originalLimit := MaxQueuedSchoolTasksPerUser
	MaxQueuedSchoolTasksPerUser = 2
	defer func() { MaxQueuedSchoolTasksPerUser = originalLimit }()

	mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-full-1", model.SchoolTaskParams{Prompt: "1"})
	mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-full-2", model.SchoolTaskParams{Prompt: "2"})
	if _, _, err := SubmitSchoolTask(env.ctxA, env.roomID, model.SchoolChannelQwen, "client-full-3", model.SchoolTaskParams{Prompt: "3"}); schoolErrorCode(t, err) != CodeSchoolQueueFull {
		t.Fatalf("队列满额应 queue_full，实际 %v", err)
	}

	// 反例：非法渠道与非法 H3 参考图数量。
	if _, _, err := SubmitSchoolTask(env.ctxB, env.roomID, model.SchoolChannel("bogus"), "client-bad-channel", model.SchoolTaskParams{Prompt: "x"}); schoolErrorCode(t, err) != contract.CodeInvalidPayload {
		t.Fatalf("非法渠道应 invalid_payload，实际 %v", err)
	}
	if _, _, err := SubmitSchoolTask(env.ctxB, env.roomID, model.SchoolChannelH3, "client-bad-h3", model.SchoolTaskParams{Prompt: "x", VideoSeconds: 5}); schoolErrorCode(t, err) != contract.CodeInvalidPayload {
		t.Fatalf("H3 缺参考图应 invalid_payload，实际 %v", err)
	}

	// 反例：viewer 无权提交。
	viewer := newID("school-viewer")
	now := time.Now().UTC()
	if err := repository.SaveRoomMember(model.RoomMember{
		RoomID: env.roomID, UserID: viewer, Role: model.RoomRoleViewer, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入 viewer 成员失败: %v", err)
	}
	if _, _, err := SubmitSchoolTask(collabContext(viewer), env.roomID, model.SchoolChannelQwen, "client-viewer", model.SchoolTaskParams{Prompt: "x"}); !errors.Is(err, ErrSchoolTaskForbidden) {
		t.Fatalf("viewer 提交应 ErrSchoolTaskForbidden，实际 %v", err)
	}

	// 反例：非成员提交 → not_found。
	if _, _, err := SubmitSchoolTask(collabContext(newID("school-nonmember")), env.roomID, model.SchoolChannelQwen, "client-nonmember", model.SchoolTaskParams{Prompt: "x"}); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("非成员提交应 ErrRoomNotFound，实际 %v", err)
	}
}

// TestMockSchoolUpstreamClient 验证 mock 上游执行器接口的最小实现。
func TestMockSchoolUpstreamClient(t *testing.T) {
	client := NewMockSchoolUpstreamClient()
	submitted, err := client.Submit(context.Background(), model.SchoolTask{ID: "school-x"})
	if err != nil {
		t.Fatalf("mock 提交失败: %v", err)
	}
	if submitted.UpstreamTaskID == "" || submitted.Status != model.SchoolTaskRunning {
		t.Fatalf("mock 提交应返回上游任务号与 running 状态，实际 %+v", submitted)
	}
	queried, err := client.Query(context.Background(), submitted.UpstreamTaskID)
	if err != nil {
		t.Fatalf("mock 查询失败: %v", err)
	}
	if queried.Status != model.SchoolTaskSucceeded || queried.Progress != 100 || len(queried.ResultURLs) == 0 {
		t.Fatalf("mock 查询应返回成功与产物，实际 %+v", queried)
	}
	urls, err := client.Result(context.Background(), submitted.UpstreamTaskID)
	if err != nil || len(urls) != len(queried.ResultURLs) {
		t.Fatalf("mock 取结果失败: %v urls=%v", err, urls)
	}
	if _, err := client.Query(context.Background(), "missing"); err == nil {
		t.Fatal("查询不存在的 mock 上游任务应报错")
	}
}
