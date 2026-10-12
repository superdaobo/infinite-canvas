package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/service"
)

// 学校成功结果回填 HTTP 集成测试（C03-05 / #22）：设备令牌回传结果 → 校验 →
// 房间媒体 → 共享节点 → 任务 succeeded；重复回传幂等；非法结果 422。

func testResultPNG(t *testing.T) []byte {
	t.Helper()
	const encoded = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("解码测试 PNG 失败: %v", err)
	}
	return raw
}

func stubResultDownloader(t *testing.T, data []byte, contentType string) {
	t.Helper()
	original := service.SchoolResultDownloader
	service.SchoolResultDownloader = func(context.Context, string) ([]byte, string, error) {
		return data, contentType, nil
	}
	t.Cleanup(func() { service.SchoolResultDownloader = original })
}

func submitSchoolTaskHTTP(t *testing.T, env *schoolHTTPEnv, token, clientTaskID string) string {
	t.Helper()
	body := `{"channel":"qwen","clientTaskId":"` + clientTaskID + `","params":{"prompt":"海边小镇"}}`
	recorder, payload := doSchoolRequest(t, env.engine, http.MethodPost, schoolTasksURL(env.roomID), token, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("提交学校任务应 200，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
	var submit struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal(payload.Data, &submit); err != nil {
		t.Fatalf("解析提交响应失败: %v", err)
	}
	return submit.Task.ID
}

func claimSchoolTaskHTTP(t *testing.T, env *schoolHTTPEnv) {
	t.Helper()
	recorder, _ := doSchoolRequest(t, env.engine, http.MethodPost, "/api/bridge/school/claim", env.device, "{}")
	if recorder.Code != http.StatusOK {
		t.Fatalf("设备领取应 200，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
}

type resultBackfillHTTP struct {
	Task struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"task"`
	Node struct {
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		Metadata string `json:"metadata"`
	} `json:"node"`
	Idempotent bool `json:"idempotent"`
}

// TestBridgeSchoolResultBackfillsSharedNode 成功用例：结果回传后任务 succeeded、
// 房间图新增图片节点并保留 taskId/submittedBy；重复回传幂等只产生一份节点。
func TestBridgeSchoolResultBackfillsSharedNode(t *testing.T) {
	env := setupSchoolHTTPEnv(t)
	config.Cfg.MediaDir = t.TempDir()
	stubResultDownloader(t, testResultPNG(t), "image/png")

	taskID := submitSchoolTaskHTTP(t, env, env.owner.Token, "http-result-1")
	claimSchoolTaskHTTP(t, env)

	body := `{"taskId":"` + taskID + `","resultUrls":["https://example.invalid/result-0.png"]}`
	recorder, payload := doSchoolRequest(t, env.engine, http.MethodPost, "/api/bridge/school/result", env.device, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("结果回传应 200，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
	var backfill resultBackfillHTTP
	if err := json.Unmarshal(payload.Data, &backfill); err != nil {
		t.Fatalf("解析回填响应失败: %v raw=%s", err, string(payload.Data))
	}
	if backfill.Task.Status != string(model.SchoolTaskSucceeded) {
		t.Fatalf("任务应 succeeded，实际 %s", backfill.Task.Status)
	}
	if backfill.Node.Kind != "image" {
		t.Fatalf("应生成图片节点，实际 %s", backfill.Node.Kind)
	}
	if !strings.Contains(backfill.Node.Metadata, taskID) || !strings.Contains(backfill.Node.Metadata, env.owner.User.ID) {
		t.Fatalf("节点应保留任务与提交人关联，实际 %s", backfill.Node.Metadata)
	}
	if backfill.Idempotent {
		t.Fatal("首次回填不应幂等")
	}

	// 重复回传：幂等命中，不产生第二份节点。
	recorder, payload = doSchoolRequest(t, env.engine, http.MethodPost, "/api/bridge/school/result", env.device, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("重复结果回传应 200，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
	var repeat resultBackfillHTTP
	_ = json.Unmarshal(payload.Data, &repeat)
	if !repeat.Idempotent || repeat.Node.ID != backfill.Node.ID {
		t.Fatalf("重复回传应幂等命中同一节点，实际 %+v", repeat)
	}

	recorder, payload = doSchoolRequest(t, env.engine, http.MethodGet, "/api/v1/collab/rooms/"+env.roomID+"/graph", env.owner.Token, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("读取房间图应 200，实际 %d", recorder.Code)
	}
	var graph struct {
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(payload.Data, &graph); err != nil {
		t.Fatalf("解析房间图失败: %v", err)
	}
	if len(graph.Nodes) != 1 || graph.Nodes[0].ID != backfill.Node.ID {
		t.Fatalf("房间图应只含 1 个结果节点，实际 %+v", graph.Nodes)
	}
}

// TestBridgeSchoolResultInvalidTypeKeepsRunning 反例：结果类型不匹配 → 422，
// 任务保持 running，不创建节点、不释放单执行槽。
func TestBridgeSchoolResultInvalidTypeKeepsRunning(t *testing.T) {
	env := setupSchoolHTTPEnv(t)
	config.Cfg.MediaDir = t.TempDir()
	stubResultDownloader(t, []byte("not-an-image"), "text/plain")

	taskID := submitSchoolTaskHTTP(t, env, env.owner.Token, "http-result-bad")
	claimSchoolTaskHTTP(t, env)

	body := `{"taskId":"` + taskID + `","resultUrls":["https://example.invalid/result-0.txt"]}`
	recorder, payload := doSchoolRequest(t, env.engine, http.MethodPost, "/api/bridge/school/result", env.device, body)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("非法结果类型应 422，实际 %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(string(payload.Data), service.CodeSchoolResultInvalid) {
		t.Fatalf("应返回 result_invalid 码，实际 %s", string(payload.Data))
	}

	// 任务仍 running：设备再次领取被 blocked（单执行槽未释放）。
	recorder, payload = doSchoolRequest(t, env.engine, http.MethodPost, "/api/bridge/school/claim", env.device, "{}")
	if recorder.Code != http.StatusOK {
		t.Fatalf("领取应 200，实际 %d", recorder.Code)
	}
	var claim struct {
		Task          json.RawMessage `json:"task"`
		Blocked       bool            `json:"blocked"`
		RunningTaskID string          `json:"runningTaskId"`
	}
	if err := json.Unmarshal(payload.Data, &claim); err != nil {
		t.Fatalf("解析领取响应失败: %v", err)
	}
	if string(claim.Task) != "null" || !claim.Blocked || claim.RunningTaskID != taskID {
		t.Fatalf("非法结果不得解锁下一个任务，实际 %+v", claim)
	}
}
