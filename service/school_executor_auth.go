package service

import (
	"context"
	"crypto/subtle"
	"strings"

	"github.com/tigerowo/infinite-canvas/config"
)

// 学校受限执行设备的身份认证（C03-02 / #18，mock）。
//
// 设备身份与用户 JWT 完全分离：设备持有独立令牌（参照 Comfy Bridge 先例），
// 只允许访问设备受限任务 API。本期为 mock 静态令牌；#19 落地真实设备注册、
// 令牌签发/轮换/吊销后替换实现。设备令牌绝不落库、不落日志、不进仓库。

// SchoolExecutor 是受限执行设备身份。
type SchoolExecutor struct {
	ID         string
	OwnerScope string
	OwnerID    string
}

// SchoolExecutorAuthenticator 认证设备令牌并返回受限设备身份。
type SchoolExecutorAuthenticator interface {
	AuthenticateSchoolExecutor(ctx context.Context, bearerToken string) (SchoolExecutor, error)
}

// SchoolExecutorAuth 是当前生效的设备认证实现；测试可替换。
var SchoolExecutorAuth SchoolExecutorAuthenticator = staticSchoolExecutorAuthenticator{}

// AuthenticateSchoolExecutor 用当前实现认证 Bearer 令牌；失败返回
// ErrSchoolExecutorUnauthorized。
func AuthenticateSchoolExecutor(ctx context.Context, bearerToken string) (SchoolExecutor, error) {
	return SchoolExecutorAuth.AuthenticateSchoolExecutor(ctx, bearerToken)
}

// staticSchoolExecutorAuthenticator 基于配置的静态令牌（mock）。
type staticSchoolExecutorAuthenticator struct{}

func (staticSchoolExecutorAuthenticator) AuthenticateSchoolExecutor(_ context.Context, bearerToken string) (SchoolExecutor, error) {
	expected := strings.TrimSpace(config.Cfg.SchoolExecutorToken)
	token := strings.TrimSpace(bearerToken)
	if expected == "" || token == "" {
		return SchoolExecutor{}, ErrSchoolExecutorUnauthorized
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
		return SchoolExecutor{}, ErrSchoolExecutorUnauthorized
	}
	id := strings.TrimSpace(config.Cfg.SchoolExecutorID)
	if id == "" {
		id = "school-device-local"
	}
	return SchoolExecutor{ID: id, OwnerScope: "system", OwnerID: "school"}, nil
}
