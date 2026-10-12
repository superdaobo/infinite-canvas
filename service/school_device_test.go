package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 学校受限设备身份与账号绑定测试（C03-03 / #19）：注册/轮换/吊销、正反认证、
// 越权列举被拒、账号变化阻止旧任务重提交、恢复语义与槽位保留。
// 全部只走 mock 上游与本地内存库，不接触真实学校设备/凭据。

const schoolDeviceTestSecret = "c03-03-device-registration-secret"

// setupSchoolDeviceEnv 复用 #18 的房间/成员环境并清空设备表、开启设备注册。
func setupSchoolDeviceEnv(t *testing.T) *schoolTestEnv {
	t.Helper()
	env := setupSchoolTestEnv(t)
	db, err := repository.DB()
	if err != nil {
		t.Fatalf("获取测试库失败: %v", err)
	}
	if err := db.Exec("DELETE FROM school_devices").Error; err != nil {
		t.Fatalf("清空 school_devices 失败: %v", err)
	}
	config.Cfg.SchoolDeviceRegistrationSecret = schoolDeviceTestSecret
	config.Cfg.SchoolExecutorToken = ""
	config.Cfg.SchoolExecutorID = ""
	return env
}

func registerTestDevice(t *testing.T, name, accountRef string) (model.SchoolDevice, string) {
	t.Helper()
	device, token, err := RegisterSchoolDevice(context.Background(), schoolDeviceTestSecret,
		SchoolDeviceRegistrationInput{Name: name, SchoolAccountRef: accountRef})
	if err != nil {
		t.Fatalf("注册学校设备失败: %v", err)
	}
	return device, token
}

// TestSchoolDeviceRegisterRotateRevokeAuth 覆盖设备身份生命周期与正反认证。
func TestSchoolDeviceRegisterRotateRevokeAuth(t *testing.T) {
	setupSchoolDeviceEnv(t)

	// 反例：未配置注册密钥时注册关闭。
	config.Cfg.SchoolDeviceRegistrationSecret = ""
	if _, _, err := RegisterSchoolDevice(context.Background(), "whatever", SchoolDeviceRegistrationInput{}); schoolErrorCode(t, err) != CodeSchoolDeviceRegistrationClosed {
		t.Fatalf("注册关闭应返回 device_registration_closed，实际 %v", err)
	}
	config.Cfg.SchoolDeviceRegistrationSecret = schoolDeviceTestSecret

	// 反例：注册密钥错误 → forbidden。
	if _, _, err := RegisterSchoolDevice(context.Background(), "wrong-secret", SchoolDeviceRegistrationInput{}); schoolErrorCode(t, err) != contract.CodeForbidden {
		t.Fatalf("错误注册密钥应 forbidden，实际 %v", err)
	}

	// 正例：注册返回一次性令牌明文，设备归属为受控系统级，服务端只存摘要。
	device, token := registerTestDevice(t, "e2e-device", "acct-A")
	if device.OwnerScope != model.SchoolDeviceOwnerScopeSystem || device.OwnerID != model.SchoolDeviceOwnerID {
		t.Fatalf("设备归属应为受控系统级，实际 %+v", device)
	}
	if len(token) != model.SchoolDeviceTokenHexLength {
		t.Fatalf("令牌应为 %d 位十六进制，实际 %d", model.SchoolDeviceTokenHexLength, len(token))
	}
	if device.TokenHash == token || device.TokenHash != HashSchoolDeviceToken(token) {
		t.Fatalf("服务端只应存令牌摘要，实际 hash=%s", device.TokenHash)
	}
	raw, err := json.Marshal(device)
	if err != nil {
		t.Fatalf("序列化设备失败: %v", err)
	}
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "tokenHash") || strings.Contains(string(raw), device.TokenHash) {
		t.Fatalf("设备对外视图不得泄漏令牌或摘要，实际 %s", string(raw))
	}

	// 正例：令牌认证返回受限设备身份。
	executor, err := AuthenticateSchoolExecutor(context.Background(), token)
	if err != nil || executor.ID != device.ID || executor.OwnerScope != model.SchoolDeviceOwnerScopeSystem || executor.SchoolAccountRef != "acct-A" {
		t.Fatalf("设备令牌认证结果不正确: %+v err=%v", executor, err)
	}

	// 反例：未知令牌 → 未授权（静态令牌为空）。
	if _, err := AuthenticateSchoolExecutor(context.Background(), "deadbeef"); !errors.Is(err, ErrSchoolExecutorUnauthorized) {
		t.Fatalf("未知令牌应未授权，实际 %v", err)
	}

	// 轮换：旧令牌立即失效，新令牌可用。
	if _, newToken, err := RotateSchoolDeviceToken(context.Background(), device.ID); err != nil {
		t.Fatalf("轮换令牌失败: %v", err)
	} else if _, err := AuthenticateSchoolExecutor(context.Background(), token); !errors.Is(err, ErrSchoolExecutorUnauthorized) {
		t.Fatalf("轮换后旧令牌应失效，实际 %v", err)
	} else if _, err := AuthenticateSchoolExecutor(context.Background(), newToken); err != nil {
		t.Fatalf("轮换后新令牌应可用，实际 %v", err)
	}

	// 吊销：认证立即失败。
	if _, err := RevokeSchoolDevice(context.Background(), device.ID); err != nil {
		t.Fatalf("吊销设备失败: %v", err)
	}
	if _, err := AuthenticateSchoolExecutor(context.Background(), token); !errors.Is(err, ErrSchoolExecutorUnauthorized) {
		t.Fatalf("吊销后令牌应未授权，实际 %v", err)
	}
	if _, err := RevokeSchoolDevice(context.Background(), "sdev_missing"); schoolErrorCode(t, err) != CodeSchoolDeviceNotFound {
		t.Fatalf("吊销不存在设备应 device_not_found，实际 %v", err)
	}

	// 反例：轮换不存在设备 → device_not_found。
	if _, _, err := RotateSchoolDeviceToken(context.Background(), "sdev_missing"); schoolErrorCode(t, err) != CodeSchoolDeviceNotFound {
		t.Fatalf("轮换不存在设备应 device_not_found，实际 %v", err)
	}
}

// TestSchoolDeviceStaticTokenFallback 验证配置静态令牌（bootstrap）仍可作为回退通道。
func TestSchoolDeviceStaticTokenFallback(t *testing.T) {
	setupSchoolDeviceEnv(t)
	config.Cfg.SchoolExecutorToken = "static-bootstrap-token"
	config.Cfg.SchoolExecutorID = "static-device-id"

	executor, err := AuthenticateSchoolExecutor(context.Background(), "static-bootstrap-token")
	if err != nil || executor.ID != "static-device-id" {
		t.Fatalf("静态 bootstrap 令牌应可认证，实际 %+v err=%v", executor, err)
	}
	// 数据库设备优先：注册后的设备令牌同样可认证。
	device, token := registerTestDevice(t, "db-device", "")
	if _, err := AuthenticateSchoolExecutor(context.Background(), token); err != nil {
		t.Fatalf("数据库设备令牌应可认证，实际 %v", err)
	}
	if devices, err := ListSchoolDevices(context.Background()); err != nil || len(devices) != 1 || devices[0].ID != device.ID {
		t.Fatalf("设备列表应只含已注册设备，实际 %+v err=%v", devices, err)
	}
}

// TestSchoolAccountBindingBlocksResubmit 覆盖学校账号绑定：账号变化时旧任务不能重新提交/
// 恢复，任务保持 running、单执行槽不释放、绝不重复生成。
func TestSchoolAccountBindingBlocksResubmit(t *testing.T) {
	env := setupSchoolDeviceEnv(t)
	device, token := registerTestDevice(t, "acct-device", "acct-A")
	executor, err := AuthenticateSchoolExecutor(context.Background(), token)
	if err != nil {
		t.Fatalf("设备认证失败: %v", err)
	}
	task := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-acct", model.SchoolTaskParams{Prompt: "账号绑定"})

	// 领取时把设备当前账号指纹绑定到任务。
	claim, err := ClaimSchoolTaskForDevice(context.Background(), executor)
	if err != nil || claim.Task == nil || claim.Task.ID != task.ID {
		t.Fatalf("设备应领到任务: %+v err=%v", claim, err)
	}
	if claim.Task.SchoolAccountRef != "acct-A" {
		t.Fatalf("任务应绑定设备账号 acct-A，实际 %q", claim.Task.SchoolAccountRef)
	}

	// 正例：同账号绑定上游（重新提交）成功。
	bound, err := BindSchoolTaskUpstreamForDevice(context.Background(), executor, task.ID, "up-A")
	if err != nil || bound.UpstreamTaskID != "up-A" {
		t.Fatalf("同账号绑定上游应成功: %+v err=%v", bound, err)
	}

	// 账号切换：设备心跳上报新账号，设备记录更新。
	if _, err := HeartbeatSchoolExecutor(context.Background(), executor, "acct-B"); err != nil {
		t.Fatalf("心跳上报新账号失败: %v", err)
	}
	changed, err := AuthenticateSchoolExecutor(context.Background(), token)
	if err != nil || changed.SchoolAccountRef != "acct-B" {
		t.Fatalf("设备账号应更新为 acct-B，实际 %+v err=%v", changed, err)
	}

	// 反例：旧任务在新账号下不能重新提交/恢复。
	if _, err := BindSchoolTaskUpstreamForDevice(context.Background(), changed, task.ID, "up-B"); schoolErrorCode(t, err) != CodeSchoolAccountChanged {
		t.Fatalf("账号变化后重提交应 school_account_changed，实际 %v", err)
	}
	if _, err := RecoverRunningSchoolTaskForDevice(context.Background(), changed, task.ID, "", true); schoolErrorCode(t, err) != CodeSchoolAccountChanged {
		t.Fatalf("账号变化后恢复应 school_account_changed，实际 %v", err)
	}

	// 任务仍 running、上游号未变、单执行槽未释放。
	reloaded, ok, err := repository.FindSchoolTaskByID(task.ID)
	if err != nil || !ok || reloaded.Status != model.SchoolTaskRunning || reloaded.UpstreamTaskID != "up-A" {
		t.Fatalf("账号变化不得改写任务，实际 %+v err=%v", reloaded, err)
	}
	if other, err := ClaimSchoolTask(context.Background(), "other-device"); err != nil || other.Task != nil || !other.Blocked {
		t.Fatalf("账号变化后单执行槽不得释放，实际 %+v err=%v", other, err)
	}

	// 同账号恢复成功，保留原上游任务号。
	recovered, err := RecoverRunningSchoolTaskForDevice(context.Background(), executor, task.ID, "设备重启恢复", true)
	if err != nil || recovered.UpstreamTaskID != "up-A" || recovered.Status != model.SchoolTaskRunning {
		t.Fatalf("同账号恢复应成功并保留上游号: %+v err=%v", recovered, err)
	}

	// 心跳：新账号与旧任务绑定冲突；同账号无冲突。
	hb, err := HeartbeatSchoolExecutor(context.Background(), changed, "acct-B")
	if err != nil || !hb.AccountMismatch || hb.RunningTaskID != task.ID {
		t.Fatalf("新账号心跳应报账号冲突，实际 %+v err=%v", hb, err)
	}
	hb, err = HeartbeatSchoolExecutor(context.Background(), executor, "acct-A")
	if err != nil || hb.AccountMismatch {
		t.Fatalf("同账号心跳不应报冲突，实际 %+v err=%v", hb, err)
	}

	// 设备可显式结束旧任务释放槽位（同设备 ID，不涉及重新提交）。
	if _, err := FinishSchoolTaskForExecutor(context.Background(), executor.ID, task.ID, model.SchoolTaskFailed, "学校账号已变更，旧任务终止"); err != nil {
		t.Fatalf("结束旧任务失败: %v", err)
	}
	if final, _, _ := repository.FindSchoolTaskByID(task.ID); final.Status != model.SchoolTaskFailed {
		t.Fatalf("旧任务应被置为 failed，实际 %s", final.Status)
	}
	_ = device
}

// TestListExecutorSchoolTasksScoped 验证设备只能列举绑定到自己的任务，越权列举被拒。
func TestListExecutorSchoolTasksScoped(t *testing.T) {
	env := setupSchoolDeviceEnv(t)
	_, token1 := registerTestDevice(t, "dev-1", "acct-1")
	_, token2 := registerTestDevice(t, "dev-2", "acct-2")
	executor1, err := AuthenticateSchoolExecutor(context.Background(), token1)
	if err != nil {
		t.Fatalf("设备 1 认证失败: %v", err)
	}
	executor2, err := AuthenticateSchoolExecutor(context.Background(), token2)
	if err != nil {
		t.Fatalf("设备 2 认证失败: %v", err)
	}

	task1 := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "scope-1", model.SchoolTaskParams{Prompt: "1"})
	task2 := mustSubmit(t, env.ctxB, env, model.SchoolChannelQwen, "scope-2", model.SchoolTaskParams{Prompt: "2"})

	if claim, err := ClaimSchoolTaskForDevice(context.Background(), executor1); err != nil || claim.Task == nil || claim.Task.ID != task1.ID {
		t.Fatalf("设备 1 应领到 task1: %+v err=%v", claim, err)
	}
	if _, err := FinishSchoolTaskForExecutor(context.Background(), executor1.ID, task1.ID, model.SchoolTaskSucceeded, ""); err != nil {
		t.Fatalf("设备 1 结束 task1 失败: %v", err)
	}
	if claim, err := ClaimSchoolTaskForDevice(context.Background(), executor2); err != nil || claim.Task == nil || claim.Task.ID != task2.ID {
		t.Fatalf("设备 2 应领到 task2: %+v err=%v", claim, err)
	}
	if _, err := FinishSchoolTaskForExecutor(context.Background(), executor2.ID, task2.ID, model.SchoolTaskSucceeded, ""); err != nil {
		t.Fatalf("设备 2 结束 task2 失败: %v", err)
	}

	list1, err := ListExecutorSchoolTasks(context.Background(), executor1.ID)
	if err != nil || len(list1) != 1 || list1[0].ID != task1.ID {
		t.Fatalf("设备 1 只应看到自己的 task1，实际 %+v err=%v", list1, err)
	}
	list2, err := ListExecutorSchoolTasks(context.Background(), executor2.ID)
	if err != nil || len(list2) != 1 || list2[0].ID != task2.ID {
		t.Fatalf("设备 2 只应看到自己的 task2，实际 %+v err=%v", list2, err)
	}

	// 反例：空执行器身份越权列举被拒。
	if _, err := ListExecutorSchoolTasks(context.Background(), ""); schoolErrorCode(t, err) != CodeSchoolExecutorUnauthorized {
		t.Fatalf("空身份列举应 executor_unauthorized，实际 %v", err)
	}
}

// TestSchoolAccountMismatchRule 覆盖账号冲突纯规则（空值兼容未绑定设备）。
func TestSchoolAccountMismatchRule(t *testing.T) {
	cases := []struct {
		taskRef, deviceRef string
		want               bool
	}{
		{"acct-A", "acct-A", false},
		{"acct-A", "acct-B", true},
		{"", "acct-B", false},
		{"acct-A", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		if got := model.SchoolAccountMismatch(tc.taskRef, tc.deviceRef); got != tc.want {
			t.Errorf("SchoolAccountMismatch(%q, %q)=%v，期望 %v", tc.taskRef, tc.deviceRef, got, tc.want)
		}
	}
}
