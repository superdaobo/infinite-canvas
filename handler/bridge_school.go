package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/service"
)

// 学校受限执行设备接口（C03-02 / #18，mock）。设备身份与用户 JWT 分离，
// 由 Bearer 设备令牌认证，只允许访问本组任务 API（领取/回传/结束/恢复）。
// 本期设备为 mock，不接触真实学校设备/凭据。

// BridgeSchoolClaim 领取下一个 queued 学校任务（FIFO 单执行槽）。
func BridgeSchoolClaim(w http.ResponseWriter, r *http.Request) {
	executor, ok := authenticateSchoolExecutor(w, r)
	if !ok {
		return
	}
	claim, err := service.ClaimSchoolTaskForDevice(r.Context(), executor)
	if err != nil {
		writeCollabSchoolError(w, err)
		return
	}
	OK(w, claim)
}

// BridgeSchoolUpdate 回传上游任务号并刷新心跳。
func BridgeSchoolUpdate(w http.ResponseWriter, r *http.Request) {
	executor, ok := authenticateSchoolExecutor(w, r)
	if !ok {
		return
	}
	var request struct {
		TaskID         string `json:"taskId"`
		UpstreamTaskID string `json:"upstreamTaskId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeCollabSchoolError(w, errInvalidJSON())
		return
	}
	task, err := service.BindSchoolTaskUpstreamForDevice(r.Context(), executor, request.TaskID, request.UpstreamTaskID)
	if err != nil {
		writeCollabSchoolError(w, err)
		return
	}
	OK(w, task.DeviceView())
}

// BridgeSchoolHeartbeat 设备心跳：上报当前学校账号指纹并刷新在线时间；
// 若设备持有的 running 任务绑定的是旧账号，返回 accountMismatch=true。
func BridgeSchoolHeartbeat(w http.ResponseWriter, r *http.Request) {
	executor, ok := authenticateSchoolExecutor(w, r)
	if !ok {
		return
	}
	var request struct {
		SchoolAccountRef string `json:"schoolAccountRef"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&request)
	}
	heartbeat, err := service.HeartbeatSchoolExecutor(r.Context(), executor, request.SchoolAccountRef)
	if err != nil {
		writeCollabSchoolError(w, err)
		return
	}
	OK(w, heartbeat)
}

// BridgeSchoolTasks 设备列举自己领取过的任务（最小设备载荷）。
// 只返回绑定到该设备的任务，不返回他人任务、房间列表或任何学校凭据。
func BridgeSchoolTasks(w http.ResponseWriter, r *http.Request) {
	executor, ok := authenticateSchoolExecutor(w, r)
	if !ok {
		return
	}
	tasks, err := service.ListExecutorSchoolTasks(r.Context(), executor.ID)
	if err != nil {
		writeCollabSchoolError(w, err)
		return
	}
	OK(w, map[string]any{"items": tasks})
}

// BridgeSchoolFinish 结束自己持有的 running 任务：失败按脱敏原因记录；
// 成功且携带结果媒体时走 #22 的校验回填路径，未携带结果媒体的成功按 #18 既有语义处理。
// 需要"媒体校验通过才记成功"的执行器应改用 POST /api/bridge/school/result。
func BridgeSchoolFinish(w http.ResponseWriter, r *http.Request) {
	executor, ok := authenticateSchoolExecutor(w, r)
	if !ok {
		return
	}
	var request schoolFinishRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeCollabSchoolError(w, errInvalidJSON())
		return
	}
	if model.SchoolTaskStatus(request.Status) == model.SchoolTaskSucceeded && request.hasResult() {
		backfill, err := service.UploadSchoolTaskResult(r.Context(), executor.ID, request.TaskID, request.resultInput())
		if err != nil {
			writeCollabSchoolError(w, err)
			return
		}
		OK(w, backfill)
		return
	}
	task, err := service.FinishSchoolTaskForExecutor(r.Context(), executor.ID, request.TaskID,
		model.SchoolTaskStatus(request.Status), request.Error)
	if err != nil {
		writeCollabSchoolError(w, err)
		return
	}
	OK(w, task.DeviceView())
}

// BridgeSchoolResult 执行器回传成功结果：校验结果媒体可访问/完整 → 写入房间媒体 →
// 原子回填共享图片/视频节点并广播 → 任务置 succeeded（#22）。
func BridgeSchoolResult(w http.ResponseWriter, r *http.Request) {
	executor, ok := authenticateSchoolExecutor(w, r)
	if !ok {
		return
	}
	var request schoolFinishRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeCollabSchoolError(w, errInvalidJSON())
		return
	}
	backfill, err := service.UploadSchoolTaskResult(r.Context(), executor.ID, request.TaskID, request.resultInput())
	if err != nil {
		writeCollabSchoolError(w, err)
		return
	}
	OK(w, backfill)
}

// schoolFinishRequest 是设备结束/回传请求体：失败用 error，成功用 result* 字段。
type schoolFinishRequest struct {
	TaskID            string   `json:"taskId"`
	Status            string   `json:"status"`
	Error             string   `json:"error"`
	UpstreamTaskID    string   `json:"upstreamTaskId"`
	ResultURLs        []string `json:"resultUrls"`
	ResultFilename    string   `json:"resultFilename"`
	ResultContentType string   `json:"resultContentType"`
}

func (request schoolFinishRequest) resultInput() service.SchoolResultInput {
	return service.SchoolResultInput{
		UpstreamTaskID:    request.UpstreamTaskID,
		ResultURLs:        request.ResultURLs,
		ResultFilename:    request.ResultFilename,
		ResultContentType: request.ResultContentType,
	}
}

// hasResult 表示请求是否携带结果媒体来源（resultUrls 或上游任务号）。
func (request schoolFinishRequest) hasResult() bool {
	for _, url := range request.ResultURLs {
		if strings.TrimSpace(url) != "" {
			return true
		}
	}
	return strings.TrimSpace(request.UpstreamTaskID) != ""
}

// BridgeSchoolRecover 恢复一个 running 任务：重绑当前设备，保留原上游任务号。
func BridgeSchoolRecover(w http.ResponseWriter, r *http.Request) {
	executor, ok := authenticateSchoolExecutor(w, r)
	if !ok {
		return
	}
	var request struct {
		TaskID                 string `json:"taskId"`
		Note                   string `json:"note"`
		ConfirmPreviousStopped bool   `json:"confirmPreviousStopped"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeCollabSchoolError(w, errInvalidJSON())
		return
	}
	task, err := service.RecoverRunningSchoolTaskForDevice(r.Context(), executor, request.TaskID,
		request.Note, request.ConfirmPreviousStopped)
	if err != nil {
		writeCollabSchoolError(w, err)
		return
	}
	OK(w, task.DeviceView())
}

// authenticateSchoolExecutor 从 Bearer 设备令牌解析受限设备身份；失败 401。
func authenticateSchoolExecutor(w http.ResponseWriter, r *http.Request) (service.SchoolExecutor, bool) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	executor, err := service.AuthenticateSchoolExecutor(r.Context(), token)
	if err != nil {
		FailWithStatus(w, http.StatusUnauthorized, service.ErrSchoolExecutorUnauthorized.Error())
		return service.SchoolExecutor{}, false
	}
	return executor, true
}

func errInvalidJSON() error {
	return &contract.Error{Code: contract.CodeInvalidPayload, Message: "请求体不是合法的 JSON"}
}
