package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCollabRoutesRegisteredAndCanvasProjectsUnchanged 断言协作房间路由已注册，
// 且旧 /api/v1/canvas/projects 路由集合与行为入口保持不变。
func TestCollabRoutesRegisteredAndCanvasProjectsUnchanged(t *testing.T) {
	engine := New()
	registered := map[string]bool{}
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}

	want := []string{
		"GET /api/v1/canvas/projects",
		"POST /api/v1/canvas/projects",
		"POST /api/v1/canvas/projects/sync",
		"POST /api/v1/canvas/projects/delete",
		"GET /api/v1/collab/rooms",
		"POST /api/v1/collab/rooms",
		"GET /api/v1/collab/rooms/:id",
		"DELETE /api/v1/collab/rooms/:id",
	}
	for _, key := range want {
		if !registered[key] {
			t.Errorf("路由缺失或已被改动: %s", key)
		}
	}
}

// TestCollabAndCanvasProjectsRequireAuth 断言新旧路由都挂在同一 UserAuth 分组下：
// 未带令牌时一律返回 401，旧 /api/v1/canvas/projects 行为未变。
func TestCollabAndCanvasProjectsRequireAuth(t *testing.T) {
	engine := New()
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/collab/rooms"},
		{http.MethodPost, "/api/v1/collab/rooms"},
		{http.MethodGet, "/api/v1/collab/rooms/room-abc"},
		{http.MethodDelete, "/api/v1/collab/rooms/room-abc"},
		{http.MethodGet, "/api/v1/canvas/projects"},
		{http.MethodPost, "/api/v1/canvas/projects"},
		{http.MethodPost, "/api/v1/canvas/projects/sync"},
		{http.MethodPost, "/api/v1/canvas/projects/delete"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 未带令牌应返回 401，实际 %d", tc.method, tc.path, rec.Code)
		}
	}
}
