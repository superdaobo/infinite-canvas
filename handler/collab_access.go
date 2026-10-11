package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/service"
)

// CollabRoomMembers 列出房间成员（任何成员可见）。
func CollabRoomMembers(w http.ResponseWriter, r *http.Request, roomID string) {
	members, err := service.RoomMembers(r.Context(), roomID)
	if err != nil {
		writeCollabAccessError(w, err)
		return
	}
	OK(w, members)
}

// CollabRemoveRoomMember 移除成员（仅 owner）。
func CollabRemoveRoomMember(w http.ResponseWriter, r *http.Request, roomID, userID string) {
	if err := service.RemoveRoomMember(r.Context(), roomID, userID); err != nil {
		writeCollabAccessError(w, err)
		return
	}
	OK(w, nil)
}

// CollabUpdateRoomMemberRole 变更成员角色（仅 owner，只能设为 editor/viewer）。
func CollabUpdateRoomMemberRole(w http.ResponseWriter, r *http.Request, roomID, userID string) {
	var request struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		FailWithStatus(w, http.StatusBadRequest, service.ErrRoomInvalid.Error())
		return
	}
	member, err := service.UpdateRoomMemberRole(r.Context(), roomID, userID, request.Role)
	if err != nil {
		writeCollabAccessError(w, err)
		return
	}
	OK(w, member)
}

// CollabRoomInvites 列出房间邀请（仅 owner，不含令牌）。
func CollabRoomInvites(w http.ResponseWriter, r *http.Request, roomID string) {
	invites, err := service.RoomInvites(r.Context(), roomID)
	if err != nil {
		writeCollabAccessError(w, err)
		return
	}
	OK(w, invites)
}

// CollabCreateRoomInvite 创建邀请（仅 owner），明文令牌只在本次响应返回。
func CollabCreateRoomInvite(w http.ResponseWriter, r *http.Request, roomID string) {
	var request struct {
		Role           string `json:"role"`
		ExpiresInHours int    `json:"expiresInHours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		FailWithStatus(w, http.StatusBadRequest, service.ErrRoomInvalid.Error())
		return
	}
	if request.ExpiresInHours < 0 {
		FailWithStatus(w, http.StatusBadRequest, service.ErrRoomInvalid.Error())
		return
	}
	ttl := time.Duration(request.ExpiresInHours) * time.Hour
	invite, err := service.CreateRoomInvite(r.Context(), roomID, request.Role, ttl)
	if err != nil {
		writeCollabAccessError(w, err)
		return
	}
	OK(w, invite)
}

// CollabRevokeRoomInvite 撤销邀请（仅 owner），撤销后旧令牌立即失效。
func CollabRevokeRoomInvite(w http.ResponseWriter, r *http.Request, roomID, inviteID string) {
	if err := service.RevokeRoomInvite(r.Context(), roomID, inviteID); err != nil {
		writeCollabAccessError(w, err)
		return
	}
	OK(w, nil)
}

// CollabAcceptInvite 使用邀请令牌加入房间（需登录，令牌无效/过期/撤销均拒绝）。
func CollabAcceptInvite(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		FailWithStatus(w, http.StatusBadRequest, service.ErrInviteInvalid.Error())
		return
	}
	if strings.TrimSpace(request.Token) == "" {
		FailWithStatus(w, http.StatusForbidden, service.ErrInviteInvalid.Error())
		return
	}
	room, err := service.AcceptRoomInvite(r.Context(), request.Token)
	if err != nil {
		writeCollabAccessError(w, err)
		return
	}
	OK(w, room)
}

// writeCollabAccessError 把权限/邀请业务错误映射为统一响应结构与 HTTP 状态码。
// 注意：错误信息里绝不携带邀请令牌或哈希。
func writeCollabAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrRoomNotFound):
		FailWithStatus(w, http.StatusNotFound, err.Error())
	case errors.Is(err, service.ErrRoomForbidden), errors.Is(err, service.ErrInviteInvalid):
		FailWithStatus(w, http.StatusForbidden, err.Error())
	case errors.Is(err, service.ErrRoomInvalid):
		FailWithStatus(w, http.StatusBadRequest, err.Error())
	default:
		FailError(w, err)
	}
}
