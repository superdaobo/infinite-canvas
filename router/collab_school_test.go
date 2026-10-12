package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
	"github.com/tigerowo/infinite-canvas/service"
)

// 学校队列 HTTP 集成测试（C03-02 / #18）：用户 JWT 分组与设备受限分组分离、
// 正反用例与错误码映射。

type schoolHTTPEnv struct {
	engine   *gin.Engine
	roomID   string
	owner    model.AuthSession
	editor   model.AuthSession
	viewer   model.AuthSession
	outsider model.AuthSession
	device   string
}

func setupSchoolHTTPEnv(t *testing.T) *schoolHTTPEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	config.Cfg = config.Config{
		StorageDriver:       "sqlite",
		DatabaseDSN:         ":memory:",
		JWTSecret:           "c03-02-school-http-test-secret",
		AILogDir:            t.TempDir(),
		SchoolExecutorToken: "school-device-test-token",
		SchoolExecutorID:    "school-device-test",
	}
	db, err := repository.DB()
	if err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec("DELETE FROM school_tasks").Error; err != nil {
		t.Fatalf("清空 school_tasks 失败: %v", err)
	}

	owner := registerWSUser(t, "school_owner")
	editor := registerWSUser(t, "school_editor")
	viewer := registerWSUser(t, "school_viewer")
	outsider := registerWSUser(t, "school_outsider")
	room, err := service.CreateRoom(wsUserContext(owner.User.ID), "学校队列 HTTP 测试房间")
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	addWSMember(t, room.ID, editor.User.ID, model.RoomRoleEditor)
	addWSMember(t, room.ID, viewer.User.ID, model.RoomRoleViewer)
	return &schoolHTTPEnv{
		engine: New(), roomID: room.ID, owner: owner, editor: editor,
		viewer: viewer, outsider: outsider, device: "school-device-test-token",
	}
}

type schoolHTTPResponse struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
	Msg  string          `json:"msg"`
}

func doSchoolRequest(t *testing.T, engine *gin.Engine, method, path, bearer, body string) (*httptest.ResponseRecorder, schoolHTTPResponse) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	var payload schoolHTTPResponse
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	return recorder, payload
}

func schoolTasksURL(roomID string, parts ...string) string {
	url := "/api/v1/collab/rooms/" + roomID + "/school-tasks"
	for _, part := range parts {
		url += "/" + part
	}
	return url
}

// TestCollabSchoolRoutesRequireAuth 校验用户路由默认 401、设备路由无令牌 401。
func TestCollabSchoolRoutesRequireAuth(t *testing.T) {
	env := setupSchoolHTTPEnv(t)
	userRoutes := []struct{ method, path string }{
		{http.MethodPost, schoolTasksURL("room-x")},
		{http.MethodGet, schoolTasksURL("room-x")},
		{http.MethodPost, schoolTasksURL("room-x", "task-y", "cancel")},
	}
	for _, route := range userRoutes {
		recorder, _ := doSchoolRequest(t, env.engine, route.method, route.path, "", "")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("用户路由 %s %s 无令牌应 401，实际 %d", route.method, route.path, recorder.Code)
		}
	}
	for _, path := range []string{"/api/bridge/school/claim", "/api/bridge/school/update", "/api/bridge/school/finish", "/api/bridge/school/recover"} {
		recorder, _ := doSchoolRequest(t, env.engine, http.MethodPost, path, "", "{}")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("设备路由 %s 无令牌应 401，实际 %d", path, recorder.Code)
		}
	}
}

// TestCollabSchoolTaskHTTPFlow 覆盖提交/幂等/越权/领取/回传/结束/取消的正反用例。
func TestCollabSchoolTaskHTTPFlow(t *testing.T) {
	env := setupSchoolHTTPEnv(t)
	submitBody := `{"channel":"qwen","clientTaskId":"http-a1","params":{"prompt":"海边小镇"}}`

	// 正例：owner 提交成功。
	recorder, payload := doSchoolRequest(t, env.engine, http.MethodPost, schoolTasksURL(env.roomID), env.owner.Token, submitBody)
	if recorder.Code != http.StatusOK || payload.Code != 0 {
		t.Fatalf("owner 提交应成功，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
	var submit struct {
		Task struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"task"`
		Idempotent bool `json:"idempotent"`
	}
	if err := json.Unmarshal(payload.Data, &submit); err != nil {
		t.Fatalf("解析提交响应失败: %v raw=%s", err, string(payload.Data))
	}
	if submit.Task.ID == "" || submit.Task.Status != string(model.SchoolTaskQueued) || submit.Idempotent {
		t.Fatalf("提交结果不正确: %+v", submit)
	}
	taskID := submit.Task.ID

	// 正例：幂等重复提交命中同一任务。
	_, payload = doSchoolRequest(t, env.engine, http.MethodPost, schoolTasksURL(env.roomID), env.owner.Token, submitBody)
	if err := json.Unmarshal(payload.Data, &submit); err != nil {
		t.Fatalf("解析幂等响应失败: %v", err)
	}
	if !submit.Idempotent || submit.Task.ID != taskID {
		t.Fatalf("重复提交应幂等命中同一任务: %+v", submit)
	}

	// 反例：viewer 提交 403；非成员提交 404；非法渠道 400。
	recorder, _ = doSchoolRequest(t, env.engine, http.MethodPost, schoolTasksURL(env.roomID), env.viewer.Token, submitBody)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("viewer 提交应 403，实际 %d", recorder.Code)
	}
	recorder, _ = doSchoolRequest(t, env.engine, http.MethodPost, schoolTasksURL(env.roomID), env.outsider.Token, submitBody)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("非成员提交应 404，实际 %d", recorder.Code)
	}
	recorder, _ = doSchoolRequest(t, env.engine, http.MethodPost, schoolTasksURL(env.roomID), env.owner.Token,
		`{"channel":"bogus","clientTaskId":"http-bad","params":{"prompt":"x"}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法渠道应 400，实际 %d", recorder.Code)
	}

	// 正例：成员可读取队列。
	recorder, _ = doSchoolRequest(t, env.engine, http.MethodGet, schoolTasksURL(env.roomID), env.editor.Token, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("editor 读取队列应 200，实际 %d", recorder.Code)
	}

	// 反例：设备令牌错误 401；正例：正确设备令牌领取到该任务。
	recorder, _ = doSchoolRequest(t, env.engine, http.MethodPost, "/api/bridge/school/claim", "wrong-token", "{}")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("错误设备令牌应 401，实际 %d", recorder.Code)
	}
	recorder, payload = doSchoolRequest(t, env.engine, http.MethodPost, "/api/bridge/school/claim", env.device, "{}")
	if recorder.Code != http.StatusOK {
		t.Fatalf("设备领取应 200，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
	var claim struct {
		Task *struct {
			ID         string `json:"id"`
			ExecutorID string `json:"executorId"`
			Status     string `json:"status"`
		} `json:"task"`
		Blocked       bool   `json:"blocked"`
		RunningTaskID string `json:"runningTaskId"`
	}
	if err := json.Unmarshal(payload.Data, &claim); err != nil {
		t.Fatalf("解析领取响应失败: %v", err)
	}
	if claim.Task == nil || claim.Task.ID != taskID || claim.Task.Status != string(model.SchoolTaskRunning) {
		t.Fatalf("设备应领到该任务，实际 %+v", claim)
	}
	if claim.Task.ExecutorID != "school-device-test" {
		t.Fatalf("执行设备绑定错误: %s", claim.Task.ExecutorID)
	}

	// 反例：设备第二次领取被 blocked；回传上游任务号成功且幂等。
	_, payload = doSchoolRequest(t, env.engine, http.MethodPost, "/api/bridge/school/claim", env.device, "{}")
	_ = json.Unmarshal(payload.Data, &claim)
	if claim.Task != nil || !claim.Blocked || claim.RunningTaskID != taskID {
		t.Fatalf("running 占用时设备不应再领到任务，实际 %+v", claim)
	}
	recorder, _ = doSchoolRequest(t, env.engine, http.MethodPost, "/api/bridge/school/update", env.device,
		`{"taskId":"`+taskID+`","upstreamTaskId":"mock-upstream-http"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("设备回传上游任务号应 200，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder, _ = doSchoolRequest(t, env.engine, http.MethodPost, "/api/bridge/school/update", env.device,
		`{"taskId":"`+taskID+`","upstreamTaskId":"other-upstream"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("绑定不同上游任务号应 409，实际 %d", recorder.Code)
	}

	// 反例：editor 取消他人任务 403；owner 取消自己 running 任务 409。
	recorder, _ = doSchoolRequest(t, env.engine, http.MethodPost, schoolTasksURL(env.roomID, taskID, "cancel"), env.editor.Token, "")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("取消他人任务应 403，实际 %d", recorder.Code)
	}
	recorder, payload = doSchoolRequest(t, env.engine, http.MethodPost, schoolTasksURL(env.roomID, taskID, "cancel"), env.owner.Token, "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("取消 running 任务应 409，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(string(payload.Data), service.CodeSchoolTaskNotCancellable) {
		t.Fatalf("取消 running 应返回 not_cancellable 码，实际 %s", string(payload.Data))
	}

	// 正例：设备结束任务后，owner 取消终态任务 409，队列可见 succeeded。
	recorder, _ = doSchoolRequest(t, env.engine, http.MethodPost, "/api/bridge/school/finish", env.device,
		`{"taskId":"`+taskID+`","status":"succeeded"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("设备结束任务应 200，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder, _ = doSchoolRequest(t, env.engine, http.MethodPost, schoolTasksURL(env.roomID, taskID, "cancel"), env.owner.Token, "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("取消已结束任务应 409，实际 %d", recorder.Code)
	}
	_, payload = doSchoolRequest(t, env.engine, http.MethodGet, schoolTasksURL(env.roomID), env.owner.Token, "")
	var queue struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(payload.Data, &queue); err != nil {
		t.Fatalf("解析队列失败: %v", err)
	}
	if queue.Total != 1 || len(queue.Items) != 1 || queue.Items[0].Status != string(model.SchoolTaskSucceeded) {
		t.Fatalf("队列状态应为 succeeded，实际 %+v", queue)
	}
}
