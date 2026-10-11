package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/service"
)

// CollabSubmitSchoolTask 提交学校共享生成任务（owner/editor，幂等）。
// 响应 {code,data,msg}；契约错误码放在 data.error，HTTP 状态由 schoolErrorStatus 决定。
func CollabSubmitSchoolTask(w http.ResponseWriter, r *http.Request, roomID string) {
	var request struct {
		Channel      string                 `json:"channel"`
		ClientTaskID string                 `json:"clientTaskId"`
		Params       model.SchoolTaskParams `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeCollabSchoolError(w, &contract.Error{Code: contract.CodeInvalidPayload, Message: "请求体不是合法的 JSON"})
		return
	}
	task, idempotent, err := service.SubmitSchoolTask(r.Context(), roomID,
		model.SchoolChannel(request.Channel), request.ClientTaskID, request.Params)
	if err != nil {
		writeCollabSchoolError(w, err)
		return
	}
	OK(w, map[string]any{"task": task.PublicView(), "idempotent": idempotent})
}

// CollabRoomSchoolTasks 读取房间学校任务队列（成员可见，按 FIFO 可见顺序）。
func CollabRoomSchoolTasks(w http.ResponseWriter, r *http.Request, roomID string) {
	queue, err := service.ListRoomSchoolTasks(r.Context(), roomID)
	if err != nil {
		writeCollabSchoolError(w, err)
		return
	}
	OK(w, queue)
}

// CollabCancelSchoolTask 取消本人提交的未开始（queued）学校任务。
func CollabCancelSchoolTask(w http.ResponseWriter, r *http.Request, roomID, taskID string) {
	task, err := service.CancelSchoolTask(r.Context(), roomID, taskID)
	if err != nil {
		writeCollabSchoolError(w, err)
		return
	}
	OK(w, task.PublicView())
}

// writeCollabSchoolError 把学校队列错误映射为统一响应结构与 HTTP 状态码。
func writeCollabSchoolError(w http.ResponseWriter, err error) {
	var contractErr *contract.Error
	if errors.As(err, &contractErr) {
		details := contractErr.Details
		if details == nil {
			details = map[string]any{}
		}
		writeJSONWithStatus(w, schoolErrorStatus(contractErr.Code), response{
			Code: 1,
			Data: map[string]any{"error": contractErr.Code, "details": details},
			Msg:  contractErr.Message,
		})
		return
	}
	switch {
	case errors.Is(err, service.ErrSchoolTaskForbidden):
		FailWithStatus(w, http.StatusForbidden, err.Error())
	case errors.Is(err, service.ErrSchoolTaskNotFound):
		FailWithStatus(w, http.StatusNotFound, err.Error())
	case errors.Is(err, service.ErrSchoolExecutorUnauthorized):
		FailWithStatus(w, http.StatusUnauthorized, err.Error())
	default:
		writeCollabRoomError(w, err)
	}
}

// schoolErrorStatus 在契约状态映射基础上补充学校队列专用错误码。
func schoolErrorStatus(code string) int {
	switch code {
	case service.CodeSchoolQueueFull:
		return http.StatusTooManyRequests
	case service.CodeSchoolExecutorUnauthorized:
		return http.StatusUnauthorized
	case service.CodeSchoolTaskNotCancellable, service.CodeSchoolTaskNotRunning:
		return http.StatusConflict
	case service.CodeSchoolResultMissing:
		return http.StatusConflict
	case service.CodeSchoolResultInvalid:
		return http.StatusUnprocessableEntity
	case service.CodeSchoolResultUnreachable:
		return http.StatusBadGateway
	default:
		return contract.StatusFor(code)
	}
}
