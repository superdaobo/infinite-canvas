package router

import (
	"context"
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

// 学校受限设备执行器端到端测试（C03-03 / #19）：注册设备 → 用设备令牌驱动
// SchoolExecutorClient 完成 领取 → 提交(mock 上游) → 绑定 → 轮询 → 回传结果 → 结束，
// 并覆盖最小权限面（设备令牌不能访问用户接口）、越权列举被拒、重复执行不重复生成。
// 全程只走 mock 上游与本地假数据，不接触真实学校设备/凭据。

const executorTestRegistrationSecret = "c03-03-device-registration-secret"

type schoolExecutorEnv struct {
	server *httptest.Server
	roomID string
	owner  model.AuthSession
	editor model.AuthSession
}

func setupSchoolExecutorEnv(t *testing.T) *schoolExecutorEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	config.Cfg = config.Config{
		StorageDriver:                  "sqlite",
		DatabaseDSN:                    ":memory:",
		JWTSecret:                      "c03-03-device-executor-test-secret",
		AILogDir:                       t.TempDir(),
		MediaDir:                       t.TempDir(),
		SchoolDeviceRegistrationSecret: executorTestRegistrationSecret,
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
	for _, table := range []string{"school_tasks", "school_devices"} {
		if err := db.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("清空 %s 失败: %v", table, err)
		}
	}

	owner := registerWSUser(t, "exec_owner")
	editor := registerWSUser(t, "exec_editor")
	room, err := service.CreateRoom(wsUserContext(owner.User.ID), "设备执行器集成测试房间")
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	addWSMember(t, room.ID, editor.User.ID, model.RoomRoleEditor)

	engine := New()
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	return &schoolExecutorEnv{server: server, roomID: room.ID, owner: owner, editor: editor}
}

// stubSchoolResultDownloader 用本地假下载器替换 SSRF 结果拉取，返回合法 PNG。
func stubSchoolResultDownloader(t *testing.T) {
	t.Helper()
	original := service.SchoolResultDownloader
	service.SchoolResultDownloader = func(context.Context, string) ([]byte, string, error) {
		return pngBytes(128), "image/png", nil
	}
	t.Cleanup(func() { service.SchoolResultDownloader = original })
}

// deviceTasks 用设备令牌读取设备自己的任务列表。
func deviceTasks(t *testing.T, baseURL, token string) []model.SchoolTaskDeviceView {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/bridge/school/tasks", nil)
	if err != nil {
		t.Fatalf("构造设备任务列表请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求设备任务列表失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("设备任务列表应 200，实际 %d body=%s", resp.StatusCode, string(body))
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Items []model.SchoolTaskDeviceView `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("解析设备任务列表失败: %v", err)
	}
	return envelope.Data.Items
}

func TestSchoolDeviceExecutorEndToEnd(t *testing.T) {
	env := setupSchoolExecutorEnv(t)
	stubSchoolResultDownloader(t)

	first, _, err := service.SubmitSchoolTask(wsUserContext(env.owner.User.ID), env.roomID,
		model.SchoolChannelQwen, "e2e-1", model.SchoolTaskParams{Prompt: "设备执行器一"})
	if err != nil {
		t.Fatalf("提交任务一失败: %v", err)
	}
	second, _, err := service.SubmitSchoolTask(wsUserContext(env.editor.User.ID), env.roomID,
		model.SchoolChannelQwen, "e2e-2", model.SchoolTaskParams{Prompt: "设备执行器二"})
	if err != nil {
		t.Fatalf("提交任务二失败: %v", err)
	}

	device1, token1, err := service.RegisterSchoolDevice(context.Background(), executorTestRegistrationSecret,
		service.SchoolDeviceRegistrationInput{Name: "dev-1", SchoolAccountRef: "acct-1"})
	if err != nil {
		t.Fatalf("注册设备一失败: %v", err)
	}
	device2, token2, err := service.RegisterSchoolDevice(context.Background(), executorTestRegistrationSecret,
		service.SchoolDeviceRegistrationInput{Name: "dev-2", SchoolAccountRef: "acct-2"})
	if err != nil {
		t.Fatalf("注册设备二失败: %v", err)
	}

	// 正例：设备一完成一个完整循环。
	client1 := service.NewSchoolExecutorClient(env.server.URL, token1, "acct-1", service.NewMockSchoolUpstreamClient())
	result, err := client1.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("设备一执行循环失败: %v", err)
	}
	if !result.Claimed || result.TaskID != first.ID || result.Status != string(model.SchoolTaskSucceeded) || !result.Backfilled {
		t.Fatalf("设备一执行结果不正确: %+v", result)
	}

	// 正例：设备二完成第二个任务。
	client2 := service.NewSchoolExecutorClient(env.server.URL, token2, "acct-2", service.NewMockSchoolUpstreamClient())
	result2, err := client2.RunOnce(context.Background())
	if err != nil || !result2.Claimed || result2.TaskID != second.ID || result2.Status != string(model.SchoolTaskSucceeded) {
		t.Fatalf("设备二执行结果不正确: %+v err=%v", result2, err)
	}

	// 幂等：队列已空，设备一再次循环不应重复生成任务。
	again, err := client1.RunOnce(context.Background())
	if err != nil || again.Claimed || again.Blocked {
		t.Fatalf("队列空时重复循环不应再领取任务: %+v err=%v", again, err)
	}
	// 幂等：对已结束任务重复上报结束按幂等成功，不重复生成。
	if _, err := service.FinishSchoolTaskForExecutor(context.Background(), device1.ID, first.ID, model.SchoolTaskSucceeded, ""); err != nil {
		t.Fatalf("重复上报已结束任务应幂等成功: %v", err)
	}

	// 任务均 succeeded、单执行槽释放、执行设备绑定正确。
	queue, err := service.ListRoomSchoolTasks(wsUserContext(env.owner.User.ID), env.roomID)
	if err != nil {
		t.Fatalf("读取队列失败: %v", err)
	}
	if queue.Total != 2 || queue.Running != nil {
		t.Fatalf("两个任务应已结束且无 running，实际 %+v", queue)
	}
	for _, item := range queue.Items {
		if item.Status != model.SchoolTaskSucceeded {
			t.Fatalf("任务 %s 应为 succeeded，实际 %s", item.ID, item.Status)
		}
		if item.ID == first.ID && item.ExecutorID != device1.ID {
			t.Fatalf("任务一应绑定设备一，实际 %s", item.ExecutorID)
		}
		if item.ID == second.ID && item.ExecutorID != device2.ID {
			t.Fatalf("任务二应绑定设备二，实际 %s", item.ExecutorID)
		}
	}

	// 越权列举被拒：设备只能看到自己领取过的任务。
	list1 := deviceTasks(t, env.server.URL, token1)
	if len(list1) != 1 || list1[0].ID != first.ID {
		t.Fatalf("设备一应只看到任务一，实际 %+v", list1)
	}
	list2 := deviceTasks(t, env.server.URL, token2)
	if len(list2) != 1 || list2[0].ID != second.ID {
		t.Fatalf("设备二应只看到任务二，实际 %+v", list2)
	}

	// 最小权限面：设备令牌不能访问用户接口（无 JWT 分组 → 401）。
	req, err := http.NewRequest(http.MethodGet, env.server.URL+"/api/v1/collab/rooms", nil)
	if err != nil {
		t.Fatalf("构造用户接口请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token1)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求用户接口失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("设备令牌访问用户接口应 401，实际 %d", resp.StatusCode)
	}

	// 心跳：返回设备身份与当前账号指纹。
	heartbeat, err := client1.Heartbeat(context.Background())
	if err != nil || heartbeat.Device.ID != device1.ID || heartbeat.Device.SchoolAccountRef != "acct-1" {
		t.Fatalf("设备心跳结果不正确: %+v err=%v", heartbeat, err)
	}

	// 反例：无效设备令牌领取 401。
	badClient := service.NewSchoolExecutorClient(env.server.URL, "invalid-device-token", "acct-1", service.NewMockSchoolUpstreamClient())
	if _, err := badClient.RunOnce(context.Background()); err == nil {
		t.Fatal("无效设备令牌应返回错误")
	} else if !strings.Contains(err.Error(), "401") {
		t.Fatalf("无效设备令牌应以 401 拒绝，实际 %v", err)
	}
}
