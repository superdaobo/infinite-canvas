package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 学校受限执行设备身份（C03-03 / #19）：注册、令牌轮换、吊销、账号绑定与心跳。
//
// 设备令牌与用户 JWT 完全分离：服务端只保存令牌的 SHA-256，明文仅在注册/轮换时
// 返回一次。学校账号以不可逆指纹（SchoolAccountRef）绑定，绝不存储真实学校 Cookie、
// 登录态或 Chrome Profile。本期只做 mock：注册需要配置的注册密钥，绝不引入真实凭据。

// 学校设备相关错误码，补充契约 §8 与 #18 队列错误码未覆盖的设备场景。
const (
	// CodeSchoolDeviceRegistrationClosed 未配置注册密钥，设备注册关闭，403。
	CodeSchoolDeviceRegistrationClosed = "device_registration_closed"
	// CodeSchoolDeviceNotFound 设备不存在，404。
	CodeSchoolDeviceNotFound = "device_not_found"
	// CodeSchoolAccountChanged 任务绑定的学校账号与设备当前账号不一致，409。
	CodeSchoolAccountChanged = "school_account_changed"
)

// errSchoolDeviceTokenMiss 表示令牌未命中任何数据库设备记录；用于回退到配置静态令牌。
var errSchoolDeviceTokenMiss = errors.New("学校设备令牌未命中")

// GenerateSchoolDeviceToken 生成 32 字节随机令牌的 64 位十六进制明文。
func GenerateSchoolDeviceToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// HashSchoolDeviceToken 返回令牌的 SHA-256 十六进制摘要；服务端只存该摘要。
func HashSchoolDeviceToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// SchoolDeviceRegistrationInput 是设备注册入参。
type SchoolDeviceRegistrationInput struct {
	// Name 是设备可读名，便于运维识别；为空时使用默认名。
	Name string
	// SchoolAccountRef 是设备侧学校账号的不可逆指纹，可为空（表示暂未绑定账号）。
	SchoolAccountRef string
}

// RegisterSchoolDevice 注册一台受控的学校执行设备并返回一次性令牌明文。
// 需要配置 SCHOOL_DEVICE_REGISTRATION_SECRET；未配置时注册关闭，密钥不匹配返回 forbidden。
// 设备归属固定为受控系统级（OwnerScope=system），不允许任意用户注册为学校设备。
func RegisterSchoolDevice(ctx context.Context, registrationSecret string, input SchoolDeviceRegistrationInput) (model.SchoolDevice, string, error) {
	expected := strings.TrimSpace(config.Cfg.SchoolDeviceRegistrationSecret)
	if expected == "" {
		return model.SchoolDevice{}, "", contractFailure(CodeSchoolDeviceRegistrationClosed,
			"学校设备注册未开启", nil)
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(registrationSecret)), []byte(expected)) != 1 {
		return model.SchoolDevice{}, "", contractFailure(contract.CodeForbidden, "学校设备注册密钥无效", nil)
	}
	token, err := GenerateSchoolDeviceToken()
	if err != nil {
		return model.SchoolDevice{}, "", err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "school-device"
	}
	now := time.Now().UTC()
	device := model.SchoolDevice{
		ID:               "sdev_" + HashSchoolDeviceToken(token)[:24],
		Name:             name,
		TokenHash:        HashSchoolDeviceToken(token),
		OwnerScope:       model.SchoolDeviceOwnerScopeSystem,
		OwnerID:          model.SchoolDeviceOwnerID,
		SchoolAccountRef: strings.TrimSpace(input.SchoolAccountRef),
		Enabled:          true,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	created, err := repository.InsertSchoolDevice(device)
	if err != nil {
		return model.SchoolDevice{}, "", err
	}
	if !created {
		return model.SchoolDevice{}, "", contractFailure(contract.CodeVersionConflict, "学校设备 ID 冲突，请重试", nil)
	}
	return device, token, nil
}

// RotateSchoolDeviceToken 轮换设备令牌：旧令牌立即失效，返回新令牌明文（仅此一次）。
func RotateSchoolDeviceToken(ctx context.Context, deviceID string) (model.SchoolDevice, string, error) {
	device, ok, err := repository.FindSchoolDeviceByID(deviceID)
	if err != nil {
		return model.SchoolDevice{}, "", err
	}
	if !ok {
		return model.SchoolDevice{}, "", contractFailure(CodeSchoolDeviceNotFound, "学校设备不存在", nil)
	}
	token, err := GenerateSchoolDeviceToken()
	if err != nil {
		return model.SchoolDevice{}, "", err
	}
	now := time.Now().UTC()
	updated, err := repository.RotateSchoolDeviceToken(device.ID, HashSchoolDeviceToken(token), now)
	if err != nil {
		return model.SchoolDevice{}, "", err
	}
	if !updated {
		return model.SchoolDevice{}, "", contractFailure(CodeSchoolDeviceNotFound, "学校设备不存在", nil)
	}
	device.RotatedAt = &now
	device.UpdatedAt = now
	return device, token, nil
}

// RevokeSchoolDevice 吊销设备令牌：认证立即失败，运行中任务仍保留（恢复语义不变）。
func RevokeSchoolDevice(ctx context.Context, deviceID string) (model.SchoolDevice, error) {
	device, ok, err := repository.FindSchoolDeviceByID(deviceID)
	if err != nil {
		return model.SchoolDevice{}, err
	}
	if !ok {
		return model.SchoolDevice{}, contractFailure(CodeSchoolDeviceNotFound, "学校设备不存在", nil)
	}
	changed, err := repository.SetSchoolDeviceEnabled(device.ID, false)
	if err != nil {
		return model.SchoolDevice{}, err
	}
	if !changed {
		return model.SchoolDevice{}, contractFailure(CodeSchoolDeviceNotFound, "学校设备不存在", nil)
	}
	device.Enabled = false
	return device, nil
}

// ListSchoolDevices 返回全部学校设备（运维/测试用，不含令牌摘要）。
func ListSchoolDevices(ctx context.Context) ([]model.SchoolDevice, error) {
	return repository.ListSchoolDevices()
}

// SchoolExecutorHeartbeatResult 是设备心跳返回：设备视图、当前持有任务与账号冲突标记。
type SchoolExecutorHeartbeatResult struct {
	Device          model.SchoolDevice `json:"device"`
	RunningTaskID   string             `json:"runningTaskId,omitempty"`
	AccountMismatch bool               `json:"accountMismatch"`
}

// HeartbeatSchoolExecutor 记录设备心跳并在账号变化时更新绑定。
// accountRef 非空且与设备当前记录不同即视为账号变化；若设备持有的 running 任务绑定的是
// 旧账号，返回 AccountMismatch=true —— 设备必须走显式恢复（同账号）或失败流程，
// 绝不能在新账号下重新提交旧任务。
func HeartbeatSchoolExecutor(ctx context.Context, executor SchoolExecutor, accountRef string) (SchoolExecutorHeartbeatResult, error) {
	executorID := strings.TrimSpace(executor.ID)
	if executorID == "" {
		return SchoolExecutorHeartbeatResult{}, contractFailure(CodeSchoolExecutorUnauthorized, "执行设备身份无效", nil)
	}
	ref := strings.TrimSpace(accountRef)
	result := SchoolExecutorHeartbeatResult{}
	device, found, err := repository.FindSchoolDeviceByID(executorID)
	if err != nil {
		return SchoolExecutorHeartbeatResult{}, err
	}
	if found {
		if ref != "" && ref != device.SchoolAccountRef {
			if _, err := repository.SetSchoolDeviceAccountRef(device.ID, ref); err != nil {
				return SchoolExecutorHeartbeatResult{}, err
			}
			device.SchoolAccountRef = ref
		}
		_, _ = repository.TouchSchoolDeviceLastSeen(device.ID, time.Now().UTC())
		result.Device = device
	} else {
		// 配置静态令牌（bootstrap）没有数据库记录，按受限身份返回最小信息。
		result.Device = model.SchoolDevice{
			ID: executorID, OwnerScope: executor.OwnerScope, OwnerID: executor.OwnerID,
			SchoolAccountRef: ref, Enabled: true,
		}
	}
	if running, ok, err := repository.FindRunningSchoolTask(); err != nil {
		return SchoolExecutorHeartbeatResult{}, err
	} else if ok && running.ExecutorID == executorID {
		result.RunningTaskID = running.ID
		result.AccountMismatch = model.SchoolAccountMismatch(running.SchoolAccountRef, ref)
	}
	return result, nil
}

// ListExecutorSchoolTasks 返回该执行设备自己领取过的任务（最小设备载荷）。
// 设备只能看到绑定到自己的任务，绝不返回其他设备或房间的任务，也不含任何学校凭据。
func ListExecutorSchoolTasks(ctx context.Context, executorID string) ([]model.SchoolTaskDeviceView, error) {
	executorID = strings.TrimSpace(executorID)
	if executorID == "" {
		return nil, contractFailure(CodeSchoolExecutorUnauthorized, "执行设备身份无效", nil)
	}
	tasks, err := repository.ListSchoolTasksByExecutor(executorID)
	if err != nil {
		return nil, err
	}
	views := make([]model.SchoolTaskDeviceView, 0, len(tasks))
	for index := range tasks {
		views = append(views, tasks[index].DeviceView())
	}
	return views, nil
}

// schoolDeviceAuthenticator 基于数据库设备记录认证令牌（C03-03 / #19）。
// 未命中返回 errSchoolDeviceTokenMiss 以便回退到配置静态令牌；命中但被吊销返回未授权。
type schoolDeviceAuthenticator struct{}

func (schoolDeviceAuthenticator) AuthenticateSchoolExecutor(_ context.Context, bearerToken string) (SchoolExecutor, error) {
	token := strings.TrimSpace(bearerToken)
	if token == "" {
		return SchoolExecutor{}, errSchoolDeviceTokenMiss
	}
	device, ok, err := repository.FindSchoolDeviceByTokenHash(HashSchoolDeviceToken(token))
	if err != nil {
		return SchoolExecutor{}, err
	}
	if !ok {
		return SchoolExecutor{}, errSchoolDeviceTokenMiss
	}
	if !device.Enabled {
		return SchoolExecutor{}, ErrSchoolExecutorUnauthorized
	}
	_, _ = repository.TouchSchoolDeviceLastSeen(device.ID, time.Now().UTC())
	return SchoolExecutor{
		ID:               device.ID,
		OwnerScope:       device.OwnerScope,
		OwnerID:          device.OwnerID,
		SchoolAccountRef: device.SchoolAccountRef,
	}, nil
}

// compositeSchoolExecutorAuthenticator 先查数据库设备记录，未命中再回退到配置静态令牌。
// 静态令牌是部署期 bootstrap 通道，保留以兼容既有部署；数据库设备支持注册/轮换/吊销。
type compositeSchoolExecutorAuthenticator struct {
	device SchoolExecutorAuthenticator
	static SchoolExecutorAuthenticator
}

func (c compositeSchoolExecutorAuthenticator) AuthenticateSchoolExecutor(ctx context.Context, bearerToken string) (SchoolExecutor, error) {
	executor, err := c.device.AuthenticateSchoolExecutor(ctx, bearerToken)
	if err == nil {
		return executor, nil
	}
	if errors.Is(err, errSchoolDeviceTokenMiss) {
		return c.static.AuthenticateSchoolExecutor(ctx, bearerToken)
	}
	return SchoolExecutor{}, err
}
