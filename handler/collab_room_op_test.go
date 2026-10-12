package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
)

const collabOpTestRoom = "room_1Ab2Cd3Ef4"

// collabOpResponse 是 {code,data,msg} 响应中测试关心的字段。
type collabOpResponse struct {
	Code int `json:"code"`
	Data struct {
		Error string `json:"error"`
	} `json:"data"`
	Msg string `json:"msg"`
}

// TestCollabApplyOperations_OversizedBodyReturns413 覆盖审查 P2 项：请求体超过
// 上限时必须在解码前被 http.MaxBytesReader 拦截，返回 413 + payload_too_large。
func TestCollabApplyOperations_OversizedBodyReturns413(t *testing.T) {
	// 构造一个语法上可继续解析、但字节数超过上限的 JSON（巨大字符串字面量），
	// 确保解码器读取到上限而不是在首个非法字符处提前报错。
	body := `{"protocolVersion":"1","padding":"` + strings.Repeat("a", maxCollabOpBodyBytes+10) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/collab/rooms/"+collabOpTestRoom+"/operations", strings.NewReader(body))
	rec := httptest.NewRecorder()

	CollabApplyOperations(rec, req, collabOpTestRoom)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限请求体应返回 413，实际 %d，body=%s", rec.Code, rec.Body.String())
	}
	var resp collabOpResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON：%v，body=%s", err, rec.Body.String())
	}
	if resp.Code != 1 || resp.Data.Error != contract.CodePayloadTooLarge {
		t.Fatalf("响应契约错误码应为 %s，实际 code=%d error=%q", contract.CodePayloadTooLarge, resp.Code, resp.Data.Error)
	}
}

// TestCollabApplyOperations_NormalPayloadPassesSizeGate 回归：正常尺寸请求体不应
// 被体积闸门误判为 413；未登录时应在体积/解码层之后由 service 返回 401。
func TestCollabApplyOperations_NormalPayloadPassesSizeGate(t *testing.T) {
	body := `{"protocolVersion":"1","roomId":"` + collabOpTestRoom + `","operationId":"op_9Zy8Xw7Vu6",` +
		`"operations":[{"type":"update","id":"img_5Kd9xQ2mNp7","version":1,"fields":{"title":"x"}}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/collab/rooms/"+collabOpTestRoom+"/operations", strings.NewReader(body))
	rec := httptest.NewRecorder()

	CollabApplyOperations(rec, req, collabOpTestRoom)

	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("正常请求体被体积闸门拒绝（413），body=%s", rec.Body.String())
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("期望未登录返回 401（证明已通过体积/解码层），实际 %d，body=%s", rec.Code, rec.Body.String())
	}
}

// TestCollabApplyOperations_InvalidJSONReturns400 回归：非法 JSON 仍返回 400 + invalid_payload。
func TestCollabApplyOperations_InvalidJSONReturns400(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/collab/rooms/"+collabOpTestRoom+"/operations", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()

	CollabApplyOperations(rec, req, collabOpTestRoom)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应返回 400，实际 %d，body=%s", rec.Code, rec.Body.String())
	}
	var resp collabOpResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if resp.Data.Error != contract.CodeInvalidPayload {
		t.Fatalf("期望错误码 %s，实际 %q", contract.CodeInvalidPayload, resp.Data.Error)
	}
}
