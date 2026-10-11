package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 邀请令牌与有效期边界。
const (
	// inviteTokenBytes 是邀请令牌的随机字节数：32 字节 = 256 位熵，不可预测。
	inviteTokenBytes = 32
	// defaultInviteTTL 是未指定有效期时的默认值（7 天）。
	defaultInviteTTL = 7 * 24 * time.Hour
	// maxInviteTTL 是邀请有效期上限（30 天）。
	maxInviteTTL = 30 * 24 * time.Hour
)

// ErrInviteInvalid 表示邀请令牌无效、已过期或已被撤销。
// 三种情况统一返回同一错误，避免向调用方泄漏邀请的具体状态。
var ErrInviteInvalid = errors.New("邀请无效或已失效")

// newInviteToken 生成密码学安全的随机邀请令牌（URL 安全，无填充）。
func newInviteToken() (string, error) {
	buf := make([]byte, inviteTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashInviteToken 计算邀请令牌的 SHA-256 哈希，数据库只保存哈希。
func hashInviteToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateRoomInvite 由 owner 创建指定角色的邀请。仅 owner 可创建；角色只能是
// editor/viewer（不能授予 owner）。明文令牌只在本次返回，之后无法再取回。
func CreateRoomInvite(ctx context.Context, roomID, role string, ttl time.Duration) (model.RoomInviteCreated, error) {
	owner, err := RequireRoomOwner(ctx, roomID)
	if err != nil {
		return model.RoomInviteCreated{}, err
	}
	inviteRole := model.RoomMemberRole(strings.TrimSpace(role))
	if !inviteRole.Inviteable() {
		return model.RoomInviteCreated{}, ErrRoomInvalid
	}
	if ttl < 0 || ttl > maxInviteTTL {
		return model.RoomInviteCreated{}, ErrRoomInvalid
	}
	if ttl == 0 {
		ttl = defaultInviteTTL
	}
	token, err := newInviteToken()
	if err != nil {
		return model.RoomInviteCreated{}, err
	}
	now := time.Now().UTC()
	invite := model.RoomInvite{
		ID:        newID("invite"),
		RoomID:    strings.TrimSpace(roomID),
		Role:      inviteRole,
		TokenHash: hashInviteToken(token),
		CreatedBy: owner.UserID,
		ExpiresAt: now.Add(ttl),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := repository.CreateRoomInvite(invite); err != nil {
		return model.RoomInviteCreated{}, err
	}
	return model.RoomInviteCreated{RoomInvite: invite, Token: token}, nil
}

// RevokeRoomInvite 由 owner 撤销邀请。撤销后旧令牌立即失效。重复撤销幂等成功。
func RevokeRoomInvite(ctx context.Context, roomID, inviteID string) error {
	if _, err := RequireRoomOwner(ctx, roomID); err != nil {
		return err
	}
	inviteID = strings.TrimSpace(inviteID)
	if inviteID == "" {
		return ErrRoomNotFound
	}
	found, err := repository.RevokeRoomInvite(strings.TrimSpace(roomID), inviteID, time.Now().UTC())
	if err != nil {
		return err
	}
	if !found {
		return ErrRoomNotFound
	}
	return nil
}

// RoomInvites 列出房间邀请（仅 owner 可见，且不含令牌与哈希）。
func RoomInvites(ctx context.Context, roomID string) (model.RoomInviteList, error) {
	if _, err := RequireRoomOwner(ctx, roomID); err != nil {
		return model.RoomInviteList{}, err
	}
	items, err := repository.ListRoomInvites(strings.TrimSpace(roomID))
	if err != nil {
		return model.RoomInviteList{}, err
	}
	if items == nil {
		items = []model.RoomInvite{}
	}
	return model.RoomInviteList{Items: items, Total: int64(len(items))}, nil
}

// AcceptRoomInvite 使用有效邀请令牌加入房间。令牌无效、已过期或已撤销统一返回
// ErrInviteInvalid，不创建成员关系。已有成员保持原角色（不降级）。
func AcceptRoomInvite(ctx context.Context, token string) (model.RoomWithRole, error) {
	user, ok := UserFromContext(ctx)
	if !ok || strings.TrimSpace(user.ID) == "" {
		return model.RoomWithRole{}, ErrRoomNotFound
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return model.RoomWithRole{}, ErrInviteInvalid
	}
	invite, ok, err := repository.FindRoomInviteByHash(hashInviteToken(token))
	if err != nil {
		return model.RoomWithRole{}, err
	}
	if !ok || !invite.Active(time.Now().UTC()) {
		return model.RoomWithRole{}, ErrInviteInvalid
	}
	room, ok, err := repository.FindRoom(invite.RoomID)
	if err != nil {
		return model.RoomWithRole{}, err
	}
	if !ok {
		return model.RoomWithRole{}, ErrInviteInvalid
	}
	now := time.Now().UTC()
	member, err := repository.JoinRoomMemberIfAbsent(model.RoomMember{
		RoomID:    room.ID,
		UserID:    user.ID,
		Role:      invite.Role,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return model.RoomWithRole{}, err
	}
	return model.RoomWithRole{Room: room, Role: member.Role}, nil
}

// RoomMembers 列出房间成员（任何成员可见），补全用户公开信息。
func RoomMembers(ctx context.Context, roomID string) (model.RoomMemberList, error) {
	if _, err := CurrentUserRoomMember(ctx, roomID); err != nil {
		return model.RoomMemberList{}, err
	}
	members, err := repository.ListRoomMembers(strings.TrimSpace(roomID))
	if err != nil {
		return model.RoomMemberList{}, err
	}
	ids := make([]string, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.UserID)
	}
	users, err := repository.GetUsersByIDs(ids)
	if err != nil {
		return model.RoomMemberList{}, err
	}
	views := make([]model.RoomMemberView, 0, len(members))
	for _, member := range members {
		user := users[member.UserID]
		views = append(views, model.RoomMemberView{
			UserID:      member.UserID,
			Username:    user.Username,
			DisplayName: user.DisplayName,
			AvatarURL:   user.AvatarURL,
			Role:        member.Role,
			CreatedAt:   member.CreatedAt,
			UpdatedAt:   member.UpdatedAt,
		})
	}
	return model.RoomMemberList{Items: views, Total: int64(len(views))}, nil
}

// RemoveRoomMember 由 owner 移除成员。owner 不能移除自己，也不能移除 owner 角色。
// 移除后该成员立即失去房间访问权；其已创建的节点/媒体保留在房间内。
func RemoveRoomMember(ctx context.Context, roomID, targetUserID string) error {
	owner, err := RequireRoomOwner(ctx, roomID)
	if err != nil {
		return err
	}
	targetUserID = strings.TrimSpace(targetUserID)
	if targetUserID == "" {
		return ErrRoomNotFound
	}
	if targetUserID == owner.UserID {
		return ErrRoomForbidden
	}
	member, ok, err := repository.FindRoomMember(strings.TrimSpace(roomID), targetUserID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrRoomNotFound
	}
	if member.Role == model.RoomRoleOwner {
		return ErrRoomForbidden
	}
	removed, err := repository.DeleteRoomMember(strings.TrimSpace(roomID), targetUserID)
	if err != nil {
		return err
	}
	if !removed {
		return ErrRoomNotFound
	}
	return nil
}

// UpdateRoomMemberRole 由 owner 变更成员角色。目标角色只能是 editor/viewer
// （不能通过此接口授予 owner），且不能变更 owner 自己的角色。
func UpdateRoomMemberRole(ctx context.Context, roomID, targetUserID, role string) (model.RoomMemberView, error) {
	owner, err := RequireRoomOwner(ctx, roomID)
	if err != nil {
		return model.RoomMemberView{}, err
	}
	newRole := model.RoomMemberRole(strings.TrimSpace(role))
	if !newRole.Inviteable() {
		return model.RoomMemberView{}, ErrRoomInvalid
	}
	targetUserID = strings.TrimSpace(targetUserID)
	if targetUserID == "" {
		return model.RoomMemberView{}, ErrRoomNotFound
	}
	if targetUserID == owner.UserID {
		return model.RoomMemberView{}, ErrRoomForbidden
	}
	member, ok, err := repository.FindRoomMember(strings.TrimSpace(roomID), targetUserID)
	if err != nil {
		return model.RoomMemberView{}, err
	}
	if !ok {
		return model.RoomMemberView{}, ErrRoomNotFound
	}
	if member.Role == model.RoomRoleOwner {
		return model.RoomMemberView{}, ErrRoomForbidden
	}
	if _, err := repository.UpdateRoomMemberRole(strings.TrimSpace(roomID), targetUserID, newRole); err != nil {
		return model.RoomMemberView{}, err
	}
	user, _, err := repository.GetUserByID(targetUserID)
	if err != nil {
		return model.RoomMemberView{}, err
	}
	return model.RoomMemberView{
		UserID:      targetUserID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		AvatarURL:   user.AvatarURL,
		Role:        newRole,
		CreatedAt:   member.CreatedAt,
		UpdatedAt:   time.Now().UTC(),
	}, nil
}
