package service

import (
	"context"
	"crypto/subtle"
	"strings"

	"github.com/tigerowo/infinite-canvas/config"
)

// 学校受限执行设备的身份认证（C03-02 / #18，C03-03 / #19 升级为数据库设备身份）。
//
// 设备身份与用户 JWT 完全分离：设备持有独立令牌（参照 Comfy Bridge 先例），
// 只允许访问设备受限任务 API。默认实现先查数据库设备记录（支持注册/轮换/吊销），
// 未命中再回退到配置静态令牌（部署期 bootstrap）。设备令牌绝不落库明文、不落日志、不进仓库。

// SchoolExecutor 是受限执行设备身份。
type SchoolExecutor struct {
	ID         string
	OwnerScope string
	OwnerID    string
	// SchoolAccountRef 是设备侧学校账号的不可逆指纹；为空表示未绑定账号。
	SchoolAccountRef string
}

// SchoolExecutorAuthenticator 认证设备令牌并返回受限设备身份。
type SchoolExecutorAuthenticator interface {
	AuthenticateSchoolExecutor(ctx context.Context, bearerToken string) (SchoolExecutor, error)
}

// SchoolExecutorAuth 是当前生效的设备认证实现；测试可替换。
var SchoolExecutorAuth SchoolExecutorAuthenticator = compositeSchoolExecutorAuthenticator{
	device: schoolDeviceAuthenticator{},
	static: staticSchoolExecutorAuthenticator{},
}

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
