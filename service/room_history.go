package service

import (
	"context"
	"strings"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// RoomHistoryList 是房间操作历史的分页结果（审计用）。
type RoomHistoryList struct {
	Items []model.RoomHistory `json:"items"`
	Total int64               `json:"total"`
}

// ListRoomHistory 返回当前用户可访问房间的操作历史（任何成员均可审计）。
func ListRoomHistory(ctx context.Context, roomID string, limit int) (RoomHistoryList, error) {
	user, ok := UserFromContext(ctx)
	if !ok || strings.TrimSpace(user.ID) == "" {
		return RoomHistoryList{}, contractFailure(contract.CodeUnauthorized, "未登录或登录态无效", nil)
	}
	roomID = strings.TrimSpace(roomID)
	if _, ok, err := repository.FindRoomMember(roomID, user.ID); err != nil {
		return RoomHistoryList{}, err
	} else if !ok {
		return RoomHistoryList{}, contractFailure(contract.CodeNotFound, "房间不存在或无权访问", map[string]any{"roomId": roomID})
	}
	items, err := repository.ListRoomHistory(roomID, limit)
	if err != nil {
		return RoomHistoryList{}, err
	}
	if items == nil {
		items = []model.RoomHistory{}
	}
	return RoomHistoryList{Items: items, Total: int64(len(items))}, nil
}

// UndoRoomHistory 撤销当前用户自己的一条历史（按用户隔离），成功后广播变更。
func UndoRoomHistory(ctx context.Context, roomID, historyID string) (model.RoomBatchResult, error) {
	user, ok := UserFromContext(ctx)
	if !ok || strings.TrimSpace(user.ID) == "" {
		return model.RoomBatchResult{}, contractFailure(contract.CodeUnauthorized, "未登录或登录态无效", nil)
	}
	result, err := repository.UndoRoomHistory(strings.TrimSpace(roomID), user.ID, strings.TrimSpace(historyID))
	if err != nil {
		return model.RoomBatchResult{}, err
	}
	broadcastRoomChanges(roomID, result)
	return result, nil
}

// RedoRoomHistory 重做当前用户自己一条已撤销的历史（按用户隔离），成功后广播变更。
func RedoRoomHistory(ctx context.Context, roomID, historyID string) (model.RoomBatchResult, error) {
	user, ok := UserFromContext(ctx)
	if !ok || strings.TrimSpace(user.ID) == "" {
		return model.RoomBatchResult{}, contractFailure(contract.CodeUnauthorized, "未登录或登录态无效", nil)
	}
	result, err := repository.RedoRoomHistory(strings.TrimSpace(roomID), user.ID, strings.TrimSpace(historyID))
	if err != nil {
		return model.RoomBatchResult{}, err
	}
	broadcastRoomChanges(roomID, result)
	return result, nil
}
