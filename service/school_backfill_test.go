package service

import (
	"context"
	"testing"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 学校结果回填在提交人被移除/降权后仍可完成并释放单执行槽，且幂等、负例齐全。
// 对应 PR #47 审查 P1 队列堵塞项（service/school_result.go:202）。

// schoolResultInputDefault 返回一个可被假下载器满足的结果载荷。
func schoolResultInputDefault() SchoolResultInput {
	return SchoolResultInput{ResultURLs: []string{"https://example.invalid/result-0.png"}}
}

// serverCreateEnvelope 构造一个最小合法的 create 信封，用于对比普通用户写路径与服务端通道。
func serverCreateEnvelope(roomID, opID, nodeID string) contract.Envelope {
	return envelope(roomID, opID, contract.Operation{
		Type: contract.OpCreate,
		Node: &contract.Node{
			ID: nodeID, Kind: contract.NodeKindImage, Title: "通道验证节点",
			Position: contract.Position{X: 1, Y: 1}, Width: 10, Height: 10, Version: 0,
			Metadata: map[string]any{"prompt": "通道验证"},
		},
	})
}

// TestSchoolResultBackfillAfterSubmitterRemoved 回归主用例：任务执行中提交人被移出房间，
// 结果仍能回填、任务 succeeded、结果节点已建、单执行槽释放，节点保留真实 SubmittedBy。
func TestSchoolResultBackfillAfterSubmitterRemoved(t *testing.T) {
	env := setupSchoolResultEnv(t, testPNGBytes(t), "image/png", nil)
	task := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-removed",
		model.SchoolTaskParams{Prompt: "提交人将被移除"})
	next := mustSubmit(t, env.ctxB, env, model.SchoolChannelQwen, "client-removed-next",
		model.SchoolTaskParams{Prompt: "下一个"})
	if _, ok := mustClaim(t, "executor-removed"); !ok {
		t.Fatal("应能领取任务")
	}

	// 提交人 A 被房主移出房间。
	if removed, err := repository.DeleteRoomMember(env.roomID, env.userA); err != nil || !removed {
		t.Fatalf("移除提交人失败: removed=%v err=%v", removed, err)
	}
	// 回归证据：提交人被移除后，普通用户写路径确实被拒（旧实现即在此失败）。
	if _, err := ApplyRoomOperations(env.ctxA, env.roomID,
		serverCreateEnvelope(env.roomID, "op-after-removal", "img_after_removal")); contractCode(err) != contract.CodeNotFound {
		t.Fatalf("提交人被移除后普通写入应被拒（not_found），实际 %v", err)
	}

	backfill, err := UploadSchoolTaskResult(context.Background(), "executor-removed", task.ID, schoolResultInputDefault())
	if err != nil {
		t.Fatalf("提交人被移除后结果回填应成功，实际 %v", err)
	}
	if backfill.Task.Status != model.SchoolTaskSucceeded {
		t.Fatalf("任务应记为 succeeded，实际 %s", backfill.Task.Status)
	}
	metadata := nodeMetadata(t, backfill.Node)
	if metadata["submittedBy"] != env.userA {
		t.Fatalf("节点必须保留真实提交人审计 submittedBy=%s，实际 %v", env.userA, metadata["submittedBy"])
	}
	if metadata["taskId"] != task.ID {
		t.Fatalf("节点应保留 taskId=%s，实际 %v", task.ID, metadata["taskId"])
	}

	// 单执行槽已释放：下一个任务可被领取。
	claim, err := ClaimSchoolTask(context.Background(), "executor-next")
	if err != nil {
		t.Fatalf("领取下一个任务失败: %v", err)
	}
	if claim.Task == nil || claim.Task.ID != next.ID {
		t.Fatalf("单执行槽应已释放并领取下一个任务 %s，实际 %+v", next.ID, claim)
	}
}

// TestSchoolResultBackfillAfterSubmitterDowngraded 降权用例：提交人被降为 viewer，
// 结果仍能回填、任务 succeeded、单执行槽释放，节点保留真实 SubmittedBy。
func TestSchoolResultBackfillAfterSubmitterDowngraded(t *testing.T) {
	env := setupSchoolResultEnv(t, testPNGBytes(t), "image/png", nil)
	task := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-downgraded",
		model.SchoolTaskParams{Prompt: "提交人将被降权"})
	next := mustSubmit(t, env.ctxB, env, model.SchoolChannelQwen, "client-downgraded-next",
		model.SchoolTaskParams{Prompt: "下一个"})
	if _, ok := mustClaim(t, "executor-downgraded"); !ok {
		t.Fatal("应能领取任务")
	}

	// 提交人 A 被降为 viewer。
	if ok, err := repository.UpdateRoomMemberRole(env.roomID, env.userA, model.RoomRoleViewer); err != nil || !ok {
		t.Fatalf("降权提交人失败: ok=%v err=%v", ok, err)
	}
	// 回归证据：降为 viewer 后普通写路径被拒（forbidden）。
	if _, err := ApplyRoomOperations(env.ctxA, env.roomID,
		serverCreateEnvelope(env.roomID, "op-after-downgrade", "img_after_downgrade")); contractCode(err) != contract.CodeForbidden {
		t.Fatalf("提交人降为 viewer 后普通写入应被拒（forbidden），实际 %v", err)
	}

	backfill, err := UploadSchoolTaskResult(context.Background(), "executor-downgraded", task.ID, schoolResultInputDefault())
	if err != nil {
		t.Fatalf("提交人降权后结果回填应成功，实际 %v", err)
	}
	if backfill.Task.Status != model.SchoolTaskSucceeded {
		t.Fatalf("任务应记为 succeeded，实际 %s", backfill.Task.Status)
	}
	if metadata := nodeMetadata(t, backfill.Node); metadata["submittedBy"] != env.userA {
		t.Fatalf("节点必须保留真实提交人审计，实际 %v", metadata["submittedBy"])
	}

	claim, err := ClaimSchoolTask(context.Background(), "executor-next-2")
	if err != nil {
		t.Fatalf("领取下一个任务失败: %v", err)
	}
	if claim.Task == nil || claim.Task.ID != next.ID {
		t.Fatalf("单执行槽应已释放，实际 %+v", claim)
	}
}

// TestSchoolResultBackfillIdempotentAfterRevocation 幂等重报：撤权后重复回填只产生
// 一份节点与一份媒体，且不重复推进 revision。
func TestSchoolResultBackfillIdempotentAfterRevocation(t *testing.T) {
	env := setupSchoolResultEnv(t, testPNGBytes(t), "image/png", nil)
	task := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-idem-revoke",
		model.SchoolTaskParams{Prompt: "撤权幂等"})
	if _, ok := mustClaim(t, "executor-idem-revoke"); !ok {
		t.Fatal("应能领取任务")
	}
	if removed, err := repository.DeleteRoomMember(env.roomID, env.userA); err != nil || !removed {
		t.Fatalf("移除提交人失败: removed=%v err=%v", removed, err)
	}

	input := schoolResultInputDefault()
	first, err := UploadSchoolTaskResult(context.Background(), "executor-idem-revoke", task.ID, input)
	if err != nil {
		t.Fatalf("首次回填失败: %v", err)
	}
	graphFirst, err := ReadRoomGraph(env.ctxB, env.roomID)
	if err != nil {
		t.Fatalf("读取房间图失败: %v", err)
	}

	second, err := UploadSchoolTaskResult(context.Background(), "executor-idem-revoke", task.ID, input)
	if err != nil {
		t.Fatalf("重复回填应按幂等成功返回，实际 %v", err)
	}
	if !second.Idempotent {
		t.Fatal("重复回填应标记幂等命中")
	}
	if second.Node.ID != first.Node.ID {
		t.Fatalf("重复回填应命中同一节点，实际 %s vs %s", second.Node.ID, first.Node.ID)
	}

	graphSecond, err := ReadRoomGraph(env.ctxB, env.roomID)
	if err != nil {
		t.Fatalf("读取房间图失败: %v", err)
	}
	if len(graphSecond.Nodes) != 1 {
		t.Fatalf("重复回填不得产生多份节点，实际 %d", len(graphSecond.Nodes))
	}
	if graphSecond.Revision != graphFirst.Revision {
		t.Fatalf("重复回填不得推进 revision，实际 %d -> %d", graphFirst.Revision, graphSecond.Revision)
	}
	list, err := RoomMediaList(env.ctxB, env.roomID)
	if err != nil {
		t.Fatalf("读取媒体列表失败: %v", err)
	}
	if list.Total != 1 {
		t.Fatalf("重复回填不得产生多份媒体，实际 %d", list.Total)
	}
}

// TestSchoolResultBackfillRejectsNonRunningAndWrongExecutor 负例：非 running 任务、
// 非执行设备（含撤权后仍用错误设备）都不得回填，任务保持原状态、不创建节点。
func TestSchoolResultBackfillRejectsNonRunningAndWrongExecutor(t *testing.T) {
	env := setupSchoolResultEnv(t, testPNGBytes(t), "image/png", nil)
	task := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-neg",
		model.SchoolTaskParams{Prompt: "负例"})
	input := schoolResultInputDefault()

	// 负例 1：任务尚未领取（queued），任何设备都不得回填。
	if _, err := UploadSchoolTaskResult(context.Background(), "executor-neg", task.ID, input); contractCode(err) != CodeSchoolTaskNotRunning {
		t.Fatalf("非 running 任务回填应 not_running，实际 %v", err)
	}
	persisted, _, err := repository.FindSchoolTaskByID(task.ID)
	if err != nil || persisted.Status != model.SchoolTaskQueued {
		t.Fatalf("被拒后任务应保持 queued，实际 %+v err=%v", persisted, err)
	}

	if _, ok := mustClaim(t, "executor-neg-owner"); !ok {
		t.Fatal("应能领取任务")
	}
	// 负例 2：非持有任务的设备不得回填（即便提交人仍是可写成员）。
	if _, err := UploadSchoolTaskResult(context.Background(), "executor-neg-intruder", task.ID, input); contractCode(err) != CodeSchoolTaskNotRunning {
		t.Fatalf("非执行设备回填应 not_running，实际 %v", err)
	}
	// 负例 3：提交人已被移除，但由错误设备回填同样被拒。
	if removed, err := repository.DeleteRoomMember(env.roomID, env.userA); err != nil || !removed {
		t.Fatalf("移除提交人失败: removed=%v err=%v", removed, err)
	}
	if _, err := UploadSchoolTaskResult(context.Background(), "executor-neg-intruder", task.ID, input); contractCode(err) != CodeSchoolTaskNotRunning {
		t.Fatalf("撤权后错误设备回填仍应 not_running，实际 %v", err)
	}

	// 以上全部被拒后，任务保持 running，且未创建任何节点。
	persisted, _, err = repository.FindSchoolTaskByID(task.ID)
	if err != nil || persisted.Status != model.SchoolTaskRunning {
		t.Fatalf("被拒后任务应保持 running，实际 %+v err=%v", persisted, err)
	}
	graph, err := ReadRoomGraph(env.ctxB, env.roomID)
	if err != nil {
		t.Fatalf("读取房间图失败: %v", err)
	}
	if len(graph.Nodes) != 0 {
		t.Fatalf("被拒的回填不得创建节点，实际 %d", len(graph.Nodes))
	}
}

// TestApplyRoomBatchAsServerBypassesMembershipAndIsIdempotent 直接验证服务端通道：
// 成员写权限被移除后仍可写入；相同摘要幂等重放不推进 revision；不同摘要 operation_id_reused。
func TestApplyRoomBatchAsServerBypassesMembershipAndIsIdempotent(t *testing.T) {
	env := setupSchoolTestEnv(t)
	// 移除 owner A，证明服务端通道不依赖成员写权限。
	if removed, err := repository.DeleteRoomMember(env.roomID, env.userA); err != nil || !removed {
		t.Fatalf("移除成员失败: removed=%v err=%v", removed, err)
	}
	envl := serverCreateEnvelope(env.roomID, "op-server-bypass", "img_server_bypass")
	digest, err := requestDigest(envl)
	if err != nil {
		t.Fatalf("计算摘要失败: %v", err)
	}
	first, err := repository.ApplyRoomBatchAsServer(env.roomID, env.userA, envl.OperationID, digest, envl.Operations)
	if err != nil {
		t.Fatalf("服务端通道写入应成功（不依赖成员写权限），实际 %v", err)
	}
	if first.Idempotent {
		t.Fatal("首次服务端写入不应标记幂等")
	}
	if first.Revision != 1 {
		t.Fatalf("首次服务端写入应推进 revision 到 1，实际 %d", first.Revision)
	}

	// 幂等重放：同摘要命中回执，不重复推进 revision。
	second, err := repository.ApplyRoomBatchAsServer(env.roomID, env.userA, envl.OperationID, digest, envl.Operations)
	if err != nil {
		t.Fatalf("服务端通道幂等重放应成功，实际 %v", err)
	}
	if !second.Idempotent || second.Revision != first.Revision {
		t.Fatalf("幂等重放应命中回执且不推进 revision，实际 %+v", second)
	}

	// 同 operationId 不同摘要 → operation_id_reused。
	other := serverCreateEnvelope(env.roomID, "op-server-bypass", "img_server_bypass_2")
	otherDigest, err := requestDigest(other)
	if err != nil {
		t.Fatalf("计算摘要失败: %v", err)
	}
	if _, err := repository.ApplyRoomBatchAsServer(env.roomID, env.userA, envl.OperationID, otherDigest, other.Operations); contractCode(err) != contract.CodeOperationIDReused {
		t.Fatalf("同 operationId 不同摘要应 operation_id_reused，实际 %v", err)
	}

	graph, err := ReadRoomGraph(env.ctxB, env.roomID)
	if err != nil {
		t.Fatalf("读取房间图失败: %v", err)
	}
	if len(graph.Nodes) != 1 || graph.Revision != 1 {
		t.Fatalf("服务端通道应只写入 1 个节点且 revision=1，实际 nodes=%d revision=%d", len(graph.Nodes), graph.Revision)
	}
}
