package model

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
)

// assertModelCode 断言 err 是 *contract.Error 且 Code 匹配。
func assertModelCode(t *testing.T, err error, want string) *contract.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("期望契约错误 %s，实际无错误", want)
	}
	var contractErr *contract.Error
	if !errors.As(err, &contractErr) {
		t.Fatalf("期望 *contract.Error，实际 %T: %v", err, err)
	}
	if contractErr.Code != want {
		t.Fatalf("期望错误码 %s，实际 %s（%s）", want, contractErr.Code, contractErr.Message)
	}
	return contractErr
}

func h3Refs(count int) []string {
	refs := make([]string, count)
	for i := range refs {
		refs[i] = "img_ref_" + string(rune('a'+i))
	}
	return refs
}

// TestSchoolTaskIdempotencyKey_Stable 验证幂等键稳定、可区分且去除首尾空白。
func TestSchoolTaskIdempotencyKey_Stable(t *testing.T) {
	key := SchoolTaskIdempotencyKey("room-1", "user-1", "client-abc")
	if len(key) != 64 {
		t.Fatalf("幂等键应为 64 位十六进制，实际长度 %d", len(key))
	}
	if again := SchoolTaskIdempotencyKey("room-1", "user-1", "client-abc"); again != key {
		t.Fatalf("相同输入应得到相同键：%s != %s", again, key)
	}
	if trimmed := SchoolTaskIdempotencyKey(" room-1 ", " user-1 ", " client-abc "); trimmed != key {
		t.Fatalf("首尾空白应被归一：%s != %s", trimmed, key)
	}
	for _, other := range []string{
		SchoolTaskIdempotencyKey("room-2", "user-1", "client-abc"),
		SchoolTaskIdempotencyKey("room-1", "user-2", "client-abc"),
		SchoolTaskIdempotencyKey("room-1", "user-1", "client-abd"),
	} {
		if other == key {
			t.Fatalf("不同 (roomId,submittedBy,clientTaskId) 不应得到相同键：%s", other)
		}
	}
}

// TestSchoolTaskID_Deterministic 验证任务 ID 由幂等键确定性派生。
func TestSchoolTaskID_Deterministic(t *testing.T) {
	id := SchoolTaskID("room-1", "user-1", "client-abc")
	if !strings.HasPrefix(id, "school_") {
		t.Fatalf("任务 ID 应以 school_ 开头，实际 %s", id)
	}
	if id != SchoolTaskID("room-1", "user-1", "client-abc") {
		t.Fatal("同一幂等键应派生同一任务 ID")
	}
	if id == SchoolTaskID("room-1", "user-1", "client-xyz") {
		t.Fatal("不同 clientTaskId 应派生不同任务 ID")
	}
	if !contract.ValidID(id) {
		t.Fatalf("任务 ID 应为合法 ID：%s", id)
	}
}

func TestSchoolQueueScope(t *testing.T) {
	if got := SchoolQueueScope(" room-1 "); got != "school:room-1" {
		t.Fatalf("队列作用域应为 school:room-1，实际 %s", got)
	}
	if SchoolQueueScope("room-1") == SchoolQueueScope("room-2") {
		t.Fatal("不同房间应有不同学校队列作用域")
	}
}

// TestValidateSchoolChannelParams 覆盖千问与 H3 的正反例。
func TestValidateSchoolChannelParams(t *testing.T) {
	cases := []struct {
		name    string
		channel SchoolChannel
		params  SchoolTaskParams
		code    string // 空表示期望通过
	}{
		{"千问文生图", SchoolChannelQwen, SchoolTaskParams{Prompt: "海边小镇"}, ""},
		{"千问带参考图被拒", SchoolChannelQwen, SchoolTaskParams{ReferenceImages: h3Refs(1)}, contract.CodeInvalidPayload},
		{"千问带秒数被拒", SchoolChannelQwen, SchoolTaskParams{VideoSeconds: 5}, contract.CodeInvalidPayload},
		{"H3 一张参考图", SchoolChannelH3, SchoolTaskParams{ReferenceImages: h3Refs(1), VideoSeconds: 4}, ""},
		{"H3 九张参考图", SchoolChannelH3, SchoolTaskParams{ReferenceImages: h3Refs(9), VideoSeconds: 15}, ""},
		{"H3 零张参考图被拒", SchoolChannelH3, SchoolTaskParams{ReferenceImages: h3Refs(0), VideoSeconds: 5}, contract.CodeInvalidPayload},
		{"H3 十张参考图被拒", SchoolChannelH3, SchoolTaskParams{ReferenceImages: h3Refs(10), VideoSeconds: 5}, contract.CodeInvalidPayload},
		{"H3 秒数过小被拒", SchoolChannelH3, SchoolTaskParams{ReferenceImages: h3Refs(1), VideoSeconds: 3}, contract.CodeInvalidPayload},
		{"H3 秒数过大被拒", SchoolChannelH3, SchoolTaskParams{ReferenceImages: h3Refs(1), VideoSeconds: 16}, contract.CodeInvalidPayload},
		{"未知渠道被拒", SchoolChannel("doubao"), SchoolTaskParams{}, contract.CodeInvalidPayload},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSchoolChannelParams(tc.channel, tc.params)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("应通过，实际错误：%v", err)
				}
				return
			}
			assertModelCode(t, err, tc.code)
		})
	}
}

// TestSchoolTaskStateMachine 覆盖合法与非法迁移。
func TestSchoolTaskStateMachine(t *testing.T) {
	legal := [][2]SchoolTaskStatus{
		{SchoolTaskQueued, SchoolTaskRunning},
		{SchoolTaskQueued, SchoolTaskFailed},
		{SchoolTaskQueued, SchoolTaskCancelled},
		{SchoolTaskRunning, SchoolTaskSucceeded},
		{SchoolTaskRunning, SchoolTaskFailed},
		{SchoolTaskRunning, SchoolTaskCancelled},
	}
	for _, pair := range legal {
		if !CanTransitionSchoolTask(pair[0], pair[1]) {
			t.Errorf("%s -> %s 应为合法迁移", pair[0], pair[1])
		}
	}
	illegal := [][2]SchoolTaskStatus{
		{SchoolTaskQueued, SchoolTaskSucceeded},
		{SchoolTaskQueued, SchoolTaskQueued},
		{SchoolTaskRunning, SchoolTaskQueued},
		{SchoolTaskRunning, SchoolTaskRunning},
		{SchoolTaskSucceeded, SchoolTaskRunning},
		{SchoolTaskFailed, SchoolTaskRunning},
		{SchoolTaskCancelled, SchoolTaskQueued},
	}
	for _, pair := range illegal {
		if CanTransitionSchoolTask(pair[0], pair[1]) {
			t.Errorf("%s -> %s 应为非法迁移", pair[0], pair[1])
		}
	}
	if !SchoolTaskSucceeded.Terminal() || !SchoolTaskFailed.Terminal() || !SchoolTaskCancelled.Terminal() {
		t.Error("succeeded/failed/cancelled 应为终态")
	}
	if SchoolTaskQueued.Terminal() || SchoolTaskRunning.Terminal() {
		t.Error("queued/running 不应是终态")
	}

	// 返回的切片必须是副本，修改不影响内部状态机。
	next := AllowedSchoolTaskTransitions(SchoolTaskQueued)
	if len(next) != 3 {
		t.Fatalf("queued 应有 3 个后继，实际 %d", len(next))
	}
	next[0] = SchoolTaskSucceeded
	if !CanTransitionSchoolTask(SchoolTaskQueued, SchoolTaskRunning) {
		t.Fatal("修改返回副本不应影响内部状态机")
	}
	if len(AllowedSchoolTaskTransitions(SchoolTaskSucceeded)) != 0 {
		t.Fatal("终态不应有后继")
	}
}

// TestValidateSchoolTask 覆盖任务整体校验。
func TestValidateSchoolTask(t *testing.T) {
	base := SchoolTask{
		ID:           SchoolTaskID("room-1", "user-1", "client-abc"),
		RoomID:       "room-1",
		SubmittedBy:  "user-1",
		ClientTaskID: "client-abc",
		Channel:      SchoolChannelQwen,
		Status:       SchoolTaskQueued,
		Params:       SchoolTaskParams{Prompt: "p"},
	}
	if err := ValidateSchoolTask(base); err != nil {
		t.Fatalf("合法任务被拒绝：%v", err)
	}

	withExecutor := base
	withExecutor.ExecutorID = "device-1"
	if err := ValidateSchoolTask(withExecutor); err != nil {
		t.Fatalf("带执行设备身份的任务应合法：%v", err)
	}

	h3 := base
	h3.Channel = SchoolChannelH3
	h3.Params = SchoolTaskParams{ReferenceImages: h3Refs(3), VideoSeconds: 5}
	if err := ValidateSchoolTask(h3); err != nil {
		t.Fatalf("合法 H3 任务被拒绝：%v", err)
	}

	cases := []struct {
		name string
		mut  func(task *SchoolTask)
	}{
		{"房间 ID 非法", func(task *SchoolTask) { task.RoomID = "x" }},
		{"缺少提交人", func(task *SchoolTask) { task.SubmittedBy = "  " }},
		{"缺少 clientTaskId", func(task *SchoolTask) { task.ClientTaskID = "" }},
		{"未知状态", func(task *SchoolTask) { task.Status = "paused" }},
		{"执行设备身份过长", func(task *SchoolTask) { task.ExecutorID = strings.Repeat("d", contract.MaxIDLength+1) }},
		{"渠道参数越界", func(task *SchoolTask) {
			task.Channel = SchoolChannelH3
			task.Params = SchoolTaskParams{ReferenceImages: h3Refs(0), VideoSeconds: 5}
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			task := base
			tc.mut(&task)
			assertModelCode(t, ValidateSchoolTask(task), contract.CodeInvalidPayload)
		})
	}
}

// TestSchoolTaskPublicView_NoSecrets 验证公开视图不含内部上游号与参考图原值。
func TestSchoolTaskPublicView_NoSecrets(t *testing.T) {
	task := SchoolTask{
		ID:             SchoolTaskID("room-1", "user-1", "client-abc"),
		RoomID:         "room-1",
		SubmittedBy:    "user-1",
		ExecutorID:     "device-1",
		ClientTaskID:   "client-abc",
		UpstreamTaskID: "upstream-secret-xyz",
		Channel:        SchoolChannelH3,
		Status:         SchoolTaskRunning,
		Params:         SchoolTaskParams{ReferenceImages: []string{"ref-secret-url"}, VideoSeconds: 5},
		CreatedAt:      time.Now().UTC(),
	}
	view := task.PublicView()
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("公开视图序列化失败：%v", err)
	}
	encoded := string(raw)
	if strings.Contains(encoded, "upstream-secret-xyz") {
		t.Fatalf("公开视图不应包含 upstreamTaskId：%s", encoded)
	}
	if strings.Contains(encoded, "ref-secret-url") {
		t.Fatalf("公开视图不应包含参考图原值：%s", encoded)
	}
	for _, want := range []string{"room-1", "user-1", "device-1", "running"} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("公开视图应可区分 %s：%s", want, encoded)
		}
	}
}
