package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// maxRoomTitleRunes 与协作契约的节点标题上限保持一致。
const maxRoomTitleRunes = 512

// 房间业务错误。handler 依据这些哨兵错误映射 HTTP 状态码。
var (
	// ErrRoomNotFound 表示房间不存在或当前用户无权访问，统一不泄漏房间是否存在。
	ErrRoomNotFound = errors.New("房间不存在")
	// ErrRoomForbidden 表示当前用户是房间成员但无权执行该操作。
	ErrRoomForbidden = errors.New("无权操作该房间")
	// ErrRoomInvalid 表示入参或登录态无效。
	ErrRoomInvalid = errors.New("房间参数无效")
)

// CreateRoom 为当前登录用户创建共享房间，并在同一事务内写入 owner 成员关系。
func CreateRoom(ctx context.Context, title string) (model.Room, error) {
	user, ok := UserFromContext(ctx)
	if !ok || strings.TrimSpace(user.ID) == "" {
		return model.Room{}, ErrRoomInvalid
	}
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > maxRoomTitleRunes {
		return model.Room{}, ErrRoomInvalid
	}

	now := time.Now().UTC()
	room := model.Room{
		ID:        newID("room"),
		OwnerID:   user.ID,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	owner := model.RoomMember{
		RoomID:    room.ID,
		UserID:    user.ID,
		Role:      model.RoomRoleOwner,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := repository.CreateRoomWithOwner(room, owner); err != nil {
		return model.Room{}, err
	}
	return room, nil
}

// CurrentUserRooms 分页返回当前用户通过成员关系可访问的房间。
func CurrentUserRooms(ctx context.Context, query model.Query) (model.RoomList, error) {
	user, ok := UserFromContext(ctx)
	if !ok || strings.TrimSpace(user.ID) == "" {
		return model.RoomList{}, ErrRoomInvalid
	}
	query.Normalize()
	items, total, err := repository.ListRoomsByUser(user.ID, query.Offset(), query.PageSize)
	if err != nil {
		return model.RoomList{}, err
	}
	if items == nil {
		items = []model.RoomWithRole{}
	}
	return model.RoomList{Items: items, Total: total}, nil
}

// CurrentUserRoom 读取当前用户可访问的单个房间。
// 非成员与不存在的房间统一返回 ErrRoomNotFound，避免泄漏房间是否存在。
func CurrentUserRoom(ctx context.Context, roomID string) (model.RoomWithRole, error) {
	user, ok := UserFromContext(ctx)
	if !ok || strings.TrimSpace(user.ID) == "" {
		return model.RoomWithRole{}, ErrRoomNotFound
	}
	member, ok, err := repository.FindRoomMember(roomID, user.ID)
	if err != nil {
		return model.RoomWithRole{}, err
	}
	if !ok {
		return model.RoomWithRole{}, ErrRoomNotFound
	}
	room, ok, err := repository.FindRoom(roomID)
	if err != nil {
		return model.RoomWithRole{}, err
	}
	if !ok {
		return model.RoomWithRole{}, ErrRoomNotFound
	}
	return model.RoomWithRole{Room: room, Role: member.Role}, nil
}

// DeleteCurrentUserRoom 删除当前用户拥有的协作房间。
// 仅 owner 可删除：非成员返回 ErrRoomNotFound，成员但非 owner 返回 ErrRoomForbidden。
// 该操作只删除协作房间表，绝不触碰个人画布 CanvasProject。
func DeleteCurrentUserRoom(ctx context.Context, roomID string) error {
	user, ok := UserFromContext(ctx)
	if !ok || strings.TrimSpace(user.ID) == "" {
		return ErrRoomNotFound
	}
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return ErrRoomNotFound
	}
	member, ok, err := repository.FindRoomMember(roomID, user.ID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrRoomNotFound
	}
	if member.Role != model.RoomRoleOwner {
		return ErrRoomForbidden
	}
	return repository.DeleteRoomCascade(roomID)
}
