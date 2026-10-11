package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/service"
)

// CollabApplyOperations 原子提交一批房间节点/连线操作。
// 响应仍为 {code,data,msg}；契约错误码放在 data.error，HTTP 状态由 StatusFor 决定。
func CollabApplyOperations(w http.ResponseWriter, r *http.Request, roomID string) {
	var envelope contract.Envelope
	if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
		writeCollabOpError(w, &contract.Error{Code: contract.CodeInvalidPayload, Message: "请求体不是合法的 JSON"})
		return
	}
	result, err := service.ApplyRoomOperations(r.Context(), roomID, envelope)
	if err != nil {
		writeCollabOpError(w, err)
		return
	}
	OK(w, result)
}

// CollabRoomGraph 读取房间公共图快照（节点、连线与当前 revision）。
func CollabRoomGraph(w http.ResponseWriter, r *http.Request, roomID string) {
	graph, err := service.ReadRoomGraph(r.Context(), roomID)
	if err != nil {
		writeCollabOpError(w, err)
		return
	}
	OK(w, graph)
}

// writeCollabOpError 把契约错误与房间哨兵错误映射为统一响应结构 {code,data,msg}。
func writeCollabOpError(w http.ResponseWriter, err error) {
	var contractErr *contract.Error
	if errors.As(err, &contractErr) {
		details := contractErr.Details
		if details == nil {
			details = map[string]any{}
		}
		writeJSONWithStatus(w, contractErr.Status(), response{
			Code: 1,
			Data: map[string]any{"error": contractErr.Code, "details": details},
			Msg:  contractErr.Message,
		})
		return
	}
	writeCollabRoomError(w, err)
}
