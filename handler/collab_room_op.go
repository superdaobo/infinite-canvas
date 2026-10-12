package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/service"
)

// maxCollabOpBodyBytes 限制单次操作请求体的最大字节数（4 MiB）。
// 依据：单批最多 contract.MaxOperationsPerBatch(200) 个操作，每个操作可携带一个
// 节点及其 metadata；导演台 directorProject 是 public 大对象，需要留出余量。
// 4 MiB 足以覆盖正常批量提交，同时阻止任意已登录账号在房间成员校验之前用超大
// JSON 消耗内存。超限统一返回 CodePayloadTooLarge（HTTP 413）。
const maxCollabOpBodyBytes = 4 << 20

// CollabApplyOperations 原子提交一批房间节点/连线操作。
// 响应仍为 {code,data,msg}；契约错误码放在 data.error，HTTP 状态由 StatusFor 决定。
func CollabApplyOperations(w http.ResponseWriter, r *http.Request, roomID string) {
	// 在解码前限制请求体大小，避免在鉴权/成员检查之前解析任意大的 JSON。
	r.Body = http.MaxBytesReader(w, r.Body, maxCollabOpBodyBytes)
	var envelope contract.Envelope
	if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeCollabOpError(w, &contract.Error{Code: contract.CodePayloadTooLarge, Message: "请求体超过大小上限"})
			return
		}
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
