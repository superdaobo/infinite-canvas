package service

import (
	"context"
	"strings"

	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// CurrentUserRoomMember 返回当前登录用户在房间内的成员关系。
// 未登录、非成员或不存在的房间统一返回 ErrRoomNotFound，不泄漏房间是否存在。
func CurrentUserRoomMember(ctx context.Context, roomID string) (model.RoomMember, error) {
	user, ok := UserFromContext(ctx)
	if !ok || strings.TrimSpace(user.ID) == "" {
		return model.RoomMember{}, ErrRoomNotFound
	}
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return model.RoomMember{}, ErrRoomNotFound
	}
	member, ok, err := repository.FindRoomMember(roomID, user.ID)
	if err != nil {
		return model.RoomMember{}, err
	}
	if !ok {
		return model.RoomMember{}, ErrRoomNotFound
	}
	return member, nil
}

// RequireRoomWriter 校验当前用户对房间拥有写权限：owner/editor 通过，viewer 返回 ErrRoomForbidden。
// 这是节点/连线写入（#8）、媒体上传（#20）与实时写入（#10）的统一二次鉴权入口，
// 必须在每次写操作时调用，不能只在建连时校验一次。
func RequireRoomWriter(ctx context.Context, roomID string) (model.RoomMember, error) {
	member, err := CurrentUserRoomMember(ctx, roomID)
	if err != nil {
		return model.RoomMember{}, err
	}
	if !member.Role.CanWrite() {
		return model.RoomMember{}, ErrRoomForbidden
	}
	return member, nil
}

// RequireRoomOwner 校验当前用户是房间 owner：成员但非 owner（editor/viewer）返回
// ErrRoomForbidden，非成员返回 ErrRoomNotFound。
func RequireRoomOwner(ctx context.Context, roomID string) (model.RoomMember, error) {
	member, err := CurrentUserRoomMember(ctx, roomID)
	if err != nil {
		return model.RoomMember{}, err
	}
	if member.Role != model.RoomRoleOwner {
		return model.RoomMember{}, ErrRoomForbidden
	}
	return member, nil
}
