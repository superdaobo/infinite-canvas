package handler

import (
	"net/http"
	"strconv"

	"github.com/tigerowo/infinite-canvas/service"
)

// CollabRoomHistory 读取房间操作历史（审计：谁、何时、什么操作、前后版本）。
// 响应仍为 {code,data,msg}；契约错误码放在 data.error，HTTP 状态由 StatusFor 决定。
func CollabRoomHistory(w http.ResponseWriter, r *http.Request, roomID string) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := service.ListRoomHistory(r.Context(), roomID, limit)
	if err != nil {
		writeCollabOpError(w, err)
		return
	}
	OK(w, items)
}

// CollabUndoRoomHistory 撤销当前用户自己的一条历史（按用户隔离）。
func CollabUndoRoomHistory(w http.ResponseWriter, r *http.Request, roomID, historyID string) {
	result, err := service.UndoRoomHistory(r.Context(), roomID, historyID)
	if err != nil {
		writeCollabOpError(w, err)
		return
	}
	OK(w, result)
}

// CollabRedoRoomHistory 重做当前用户自己一条已撤销的历史（按用户隔离）。
func CollabRedoRoomHistory(w http.ResponseWriter, r *http.Request, roomID, historyID string) {
	result, err := service.RedoRoomHistory(r.Context(), roomID, historyID)
	if err != nil {
		writeCollabOpError(w, err)
		return
	}
	OK(w, result)
}
