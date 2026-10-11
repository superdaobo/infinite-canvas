package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestCollabAccessRoutesRequireAuth 校验权限/邀请路由已注册且都在 UserAuth 之后：
// 无令牌访问一律 401，证明路由存在且默认拒绝。
func TestCollabAccessRoutesRequireAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := New()
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/collab/rooms/room-x/members"},
		{http.MethodPatch, "/api/v1/collab/rooms/room-x/members/user-y"},
		{http.MethodDelete, "/api/v1/collab/rooms/room-x/members/user-y"},
		{http.MethodGet, "/api/v1/collab/rooms/room-x/invites"},
		{http.MethodPost, "/api/v1/collab/rooms/room-x/invites"},
		{http.MethodPost, "/api/v1/collab/rooms/room-x/invites/invite-z/revoke"},
		{http.MethodPost, "/api/v1/collab/invites/accept"},
	}
	for _, testCase := range cases {
		request := httptest.NewRequest(testCase.method, testCase.path, nil)
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 无令牌应 401，实际 %d body=%s", testCase.method, testCase.path, recorder.Code, recorder.Body.String())
		}
	}
}
