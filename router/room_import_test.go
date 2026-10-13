package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestCollabImportRouteRequiresAuth 校验旧画布导入路由已注册且在 UserAuth 之后：
// 无令牌访问一律 401，证明路由存在且默认拒绝（C04-02 / #25）。
func TestCollabImportRouteRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := New()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/collab/rooms/room-x/import", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/v1/collab/rooms/:id/import 无令牌应 401，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
}
