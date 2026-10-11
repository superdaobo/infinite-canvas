package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tigerowo/infinite-canvas/service"
)

// CollabRooms 返回当前用户可访问的协作房间分页列表。
func CollabRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := service.CurrentUserRooms(r.Context(), parseQuery(r))
	if err != nil {
		writeCollabRoomError(w, err)
		return
	}
	OK(w, rooms)
}

// CollabCreateRoom 创建共享房间。
func CollabCreateRoom(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		FailWithStatus(w, http.StatusBadRequest, service.ErrRoomInvalid.Error())
		return
	}
	room, err := service.CreateRoom(r.Context(), request.Title)
	if err != nil {
		writeCollabRoomError(w, err)
		return
	}
	OK(w, room)
}

// CollabRoomDetail 读取单个协作房间详情。
func CollabRoomDetail(w http.ResponseWriter, r *http.Request, roomID string) {
	room, err := service.CurrentUserRoom(r.Context(), roomID)
	if err != nil {
		writeCollabRoomError(w, err)
		return
	}
	OK(w, room)
}

// CollabDeleteRoom 删除协作房间（仅 owner 可删）。
func CollabDeleteRoom(w http.ResponseWriter, r *http.Request, roomID string) {
	if err := service.DeleteCurrentUserRoom(r.Context(), roomID); err != nil {
		writeCollabRoomError(w, err)
		return
	}
	OK(w, nil)
}

// writeCollabRoomError 把房间业务错误映射为统一响应结构 {code,data,msg} 与 HTTP 状态码。
func writeCollabRoomError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrRoomNotFound):
		FailWithStatus(w, http.StatusNotFound, err.Error())
	case errors.Is(err, service.ErrRoomForbidden):
		FailWithStatus(w, http.StatusForbidden, err.Error())
	case errors.Is(err, service.ErrRoomInvalid):
		FailWithStatus(w, http.StatusBadRequest, err.Error())
	default:
		FailError(w, err)
	}
}
