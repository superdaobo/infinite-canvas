package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
)

func schoolH3Refs(count int) []string {
	refs := make([]string, count)
	for i := range refs {
		refs[i] = "img_ref_" + string(rune('a'+i))
	}
	return refs
}

// TestNewSchoolTask_DerivesStableID 验证任务构造派生确定性 ID 并记录真实提交人。
func TestNewSchoolTask_DerivesStableID(t *testing.T) {
	params := model.SchoolTaskParams{Prompt: "海边小镇"}
	first, err := NewSchoolTask("room-1", "user-1", model.SchoolChannelQwen, "client-abc", params)
	if err != nil {
		t.Fatalf("构造学校任务失败：%v", err)
	}
	second, err := NewSchoolTask("room-1", "user-1", model.SchoolChannelQwen, "client-abc", params)
	if err != nil {
		t.Fatalf("重复构造学校任务失败：%v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("同一 clientTaskId 应派生同一任务 ID：%s != %s", first.ID, second.ID)
	}
	if first.ID != model.SchoolTaskID("room-1", "user-1", "client-abc") {
		t.Fatalf("任务 ID 应为确定性派生，实际 %s", first.ID)
	}
	if first.SubmittedBy != "user-1" || first.Status != model.SchoolTaskQueued || first.Channel != model.SchoolChannelQwen {
		t.Fatalf("任务字段不正确：%+v", first)
	}
	if first.ExecutorID != "" {
		t.Fatal("未领取时 executorId 应为空")
	}
}

// TestNewSchoolTask_RejectsInvalid 覆盖缺少幂等键与渠道参数越界。
func TestNewSchoolTask_RejectsInvalid(t *testing.T) {
	if _, err := NewSchoolTask("room-1", "user-1", model.SchoolChannelQwen, "  ", model.SchoolTaskParams{}); err == nil {
		t.Fatal("空 clientTaskId 应被拒绝")
	} else {
		assertContractCode(t, err, contract.CodeInvalidPayload)
	}
	// H3 零张参考图被拒。
	_, err := NewSchoolTask("room-1", "user-1", model.SchoolChannelH3, "client-h3", model.SchoolTaskParams{VideoSeconds: 5})
	assertContractCode(t, err, contract.CodeInvalidPayload)
	// 千问带参考图被拒。
	_, err = NewSchoolTask("room-1", "user-1", model.SchoolChannelQwen, "client-qwen", model.SchoolTaskParams{ReferenceImages: schoolH3Refs(1)})
	assertContractCode(t, err, contract.CodeInvalidPayload)
}

// TestResolveSchoolTaskIdempotency 覆盖重复提交的幂等与冲突分支。
func TestResolveSchoolTaskIdempotency(t *testing.T) {
	base, err := NewSchoolTask("room-1", "user-1", model.SchoolChannelQwen, "client-abc", model.SchoolTaskParams{Prompt: "p"})
	if err != nil {
		t.Fatalf("构造任务失败：%v", err)
	}

	// 首次提交：无既有任务。
	if got, idem, err := ResolveSchoolTaskIdempotency(nil, base); err != nil || idem || got.ID != base.ID {
		t.Fatalf("首次提交应原样返回，got=%+v idem=%v err=%v", got, idem, err)
	}

	// 重复提交：参数一致 -> 命中既有任务。
	existing := base
	existing.Status = model.SchoolTaskSucceeded
	got, idem, err := ResolveSchoolTaskIdempotency(&existing, base)
	if err != nil || !idem {
		t.Fatalf("重复提交应命中幂等任务，idem=%v err=%v", idem, err)
	}
	if got.Status != model.SchoolTaskSucceeded {
		t.Fatalf("应返回既有任务状态，实际 %s", got.Status)
	}

	// 同一幂等键但参数不同 -> operation_id_reused。
	different, _ := NewSchoolTask("room-1", "user-1", model.SchoolChannelQwen, "client-abc", model.SchoolTaskParams{Prompt: "改了提示词"})
	_, _, err = ResolveSchoolTaskIdempotency(&existing, different)
	assertContractCode(t, err, contract.CodeOperationIDReused)

	// 任务 ID 不一致 -> invalid_payload。
	other := base
	other.ID = model.SchoolTaskID("room-2", "user-1", "client-abc")
	_, _, err = ResolveSchoolTaskIdempotency(&existing, other)
	assertContractCode(t, err, contract.CodeInvalidPayload)
}

// TestApplySchoolTaskTransition 覆盖合法与非法状态迁移。
func TestApplySchoolTaskTransition(t *testing.T) {
	task := model.SchoolTask{Status: model.SchoolTaskQueued, UpdatedAt: time.Unix(0, 0).UTC()}
	if err := ApplySchoolTaskTransition(&task, model.SchoolTaskRunning); err != nil {
		t.Fatalf("queued -> running 应合法：%v", err)
	}
	if task.Status != model.SchoolTaskRunning {
		t.Fatalf("状态应更新为 running，实际 %s", task.Status)
	}
	if err := ApplySchoolTaskTransition(&task, model.SchoolTaskSucceeded); err != nil {
		t.Fatalf("running -> succeeded 应合法：%v", err)
	}
	// 终态再迁移被拒。
	assertContractCode(t, ApplySchoolTaskTransition(&task, model.SchoolTaskRunning), contract.CodeVersionConflict)
	// 非法跳转被拒。
	queued := model.SchoolTask{Status: model.SchoolTaskQueued}
	assertContractCode(t, ApplySchoolTaskTransition(&queued, model.SchoolTaskSucceeded), contract.CodeVersionConflict)
	// 未知状态被拒。
	assertContractCode(t, ApplySchoolTaskTransition(&queued, "paused"), contract.CodeInvalidPayload)
	// nil 任务被拒。
	assertContractCode(t, ApplySchoolTaskTransition(nil, model.SchoolTaskRunning), contract.CodeInvalidPayload)
}

// TestBindSchoolTaskUpstream 覆盖上游号首次绑定、幂等与不可替换。
func TestBindSchoolTaskUpstream(t *testing.T) {
	task := model.SchoolTask{Status: model.SchoolTaskRunning}
	if err := BindSchoolTaskUpstream(&task, "up-1"); err != nil {
		t.Fatalf("首次绑定应成功：%v", err)
	}
	if task.UpstreamTaskID != "up-1" {
		t.Fatalf("上游号应为 up-1，实际 %s", task.UpstreamTaskID)
	}
	if err := BindSchoolTaskUpstream(&task, "up-1"); err != nil {
		t.Fatalf("重复绑定同一上游号应幂等：%v", err)
	}
	assertContractCode(t, BindSchoolTaskUpstream(&task, "up-2"), contract.CodeVersionConflict)
	if task.UpstreamTaskID != "up-1" {
		t.Fatalf("冲突绑定不应改动上游号，实际 %s", task.UpstreamTaskID)
	}
	assertContractCode(t, BindSchoolTaskUpstream(&task, "  "), contract.CodeInvalidPayload)
	assertContractCode(t, BindSchoolTaskUpstream(nil, "up-1"), contract.CodeInvalidPayload)
}

// TestRequireSchoolTaskSubmitter 覆盖安全矩阵：viewer 不得提交。
func TestRequireSchoolTaskSubmitter(t *testing.T) {
	for _, role := range []model.RoomMemberRole{model.RoomRoleOwner, model.RoomRoleEditor} {
		if err := RequireSchoolTaskSubmitter(model.RoomMember{Role: role}); err != nil {
			t.Errorf("%s 应可提交学校任务：%v", role, err)
		}
	}
	for _, role := range []model.RoomMemberRole{model.RoomRoleViewer, model.RoomMemberRole(""), model.RoomMemberRole("guest")} {
		if err := RequireSchoolTaskSubmitter(model.RoomMember{Role: role}); !errors.Is(err, ErrSchoolTaskForbidden) {
			t.Errorf("%q 不应可提交学校任务，实际 %v", role, err)
		}
	}
}

// fakeSchoolUpstream 是 mock 上游接口的最小假实现，用于验证接口可用。
type fakeSchoolUpstream struct {
	submitted string
}

func (f *fakeSchoolUpstream) Submit(_ context.Context, task model.SchoolTask) (SchoolUpstreamTask, error) {
	f.submitted = task.ID
	return SchoolUpstreamTask{UpstreamTaskID: "up-" + task.ID, Status: model.SchoolTaskQueued}, nil
}

func (f *fakeSchoolUpstream) Query(_ context.Context, upstreamTaskID string) (SchoolUpstreamTask, error) {
	return SchoolUpstreamTask{UpstreamTaskID: upstreamTaskID, Status: model.SchoolTaskRunning, Progress: 50}, nil
}

func (f *fakeSchoolUpstream) Result(_ context.Context, upstreamTaskID string) ([]string, error) {
	return []string{"https://example.test/" + upstreamTaskID + ".mp4"}, nil
}

var _ SchoolUpstreamClient = (*fakeSchoolUpstream)(nil)

// TestSchoolUpstreamClientContract 证明 mock 上游接口可被假数据实现并调用。
func TestSchoolUpstreamClientContract(t *testing.T) {
	task, err := NewSchoolTask("room-1", "user-1", model.SchoolChannelQwen, "client-abc", model.SchoolTaskParams{Prompt: "p"})
	if err != nil {
		t.Fatalf("构造任务失败：%v", err)
	}
	var client SchoolUpstreamClient = &fakeSchoolUpstream{}
	submitted, err := client.Submit(context.Background(), task)
	if err != nil || submitted.UpstreamTaskID == "" {
		t.Fatalf("Submit 失败：%+v err=%v", submitted, err)
	}
	queried, err := client.Query(context.Background(), submitted.UpstreamTaskID)
	if err != nil || queried.Status != model.SchoolTaskRunning {
		t.Fatalf("Query 失败：%+v err=%v", queried, err)
	}
	urls, err := client.Result(context.Background(), submitted.UpstreamTaskID)
	if err != nil || len(urls) != 1 {
		t.Fatalf("Result 失败：%v err=%v", urls, err)
	}
}
