package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// seedRoomUser 写入一个最小用户，便于成员列表补全公开信息。
func seedRoomUser(t *testing.T, userID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := repository.SaveUser(model.User{
		ID: userID, Username: userID, DisplayName: "用户-" + userID,
		Role: model.UserRoleUser, Status: model.UserStatusActive, AffCode: "AFF-" + userID,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入测试用户 %s 失败: %v", userID, err)
	}
}

func roleOf(t *testing.T, list model.RoomMemberList, userID string) model.RoomMemberRole {
	t.Helper()
	for _, item := range list.Items {
		if item.UserID == userID {
			return item.Role
		}
	}
	t.Fatalf("成员列表缺少用户 %s: %+v", userID, list.Items)
	return ""
}

// TestCollabAccessContractSQLite 覆盖角色权限与可撤销邀请的正反用例。
func TestCollabAccessContractSQLite(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")

	ownerID := newID("access-owner")
	editorID := newID("access-editor")
	viewerID := newID("access-viewer")
	outsiderID := newID("access-outsider")
	for _, id := range []string{ownerID, editorID, viewerID, outsiderID} {
		seedRoomUser(t, id)
	}
	ctxOwner := collabContext(ownerID)
	ctxEditor := collabContext(editorID)
	ctxViewer := collabContext(viewerID)
	ctxOutsider := collabContext(outsiderID)

	room, err := CreateRoom(ctxOwner, "权限房间")
	if err != nil {
		t.Fatalf("owner 创建房间失败: %v", err)
	}

	// --- 正例：owner 可写可管；非成员一律 not_found ---
	if member, err := RequireRoomWriter(ctxOwner, room.ID); err != nil || member.Role != model.RoomRoleOwner {
		t.Fatalf("owner 应可写，实际 role=%v err=%v", member.Role, err)
	}
	if _, err := RequireRoomWriter(ctxOutsider, room.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("非成员写操作应 ErrRoomNotFound，实际 %v", err)
	}
	if _, err := RequireRoomOwner(ctxOutsider, room.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("非成员 owner 操作应 ErrRoomNotFound，实际 %v", err)
	}

	// --- 反例：owner 不能创建 owner 角色邀请，角色非法返回 ErrRoomInvalid ---
	if _, err := CreateRoomInvite(ctxOwner, room.ID, "owner", 0); !errors.Is(err, ErrRoomInvalid) {
		t.Fatalf("邀请 owner 角色应 ErrRoomInvalid，实际 %v", err)
	}
	if _, err := CreateRoomInvite(ctxOwner, room.ID, "viewer", -time.Hour); !errors.Is(err, ErrRoomInvalid) {
		t.Fatalf("负有效期应 ErrRoomInvalid，实际 %v", err)
	}
	if _, err := CreateRoomInvite(ctxOwner, room.ID, "viewer", maxInviteTTL+time.Hour); !errors.Is(err, ErrRoomInvalid) {
		t.Fatalf("超上限有效期应 ErrRoomInvalid，实际 %v", err)
	}

	// --- 正例：owner 创建 editor/viewer 邀请，令牌随机且足够长 ---
	editorInvite, err := CreateRoomInvite(ctxOwner, room.ID, "editor", 0)
	if err != nil {
		t.Fatalf("创建 editor 邀请失败: %v", err)
	}
	viewerInvite, err := CreateRoomInvite(ctxOwner, room.ID, "viewer", 0)
	if err != nil {
		t.Fatalf("创建 viewer 邀请失败: %v", err)
	}
	if editorInvite.Token == viewerInvite.Token {
		t.Fatal("两次邀请令牌不应相同")
	}
	for _, invite := range []model.RoomInviteCreated{editorInvite, viewerInvite} {
		if len(invite.Token) != 43 {
			t.Fatalf("邀请令牌长度应为 43（32 字节 base64url），实际 %d", len(invite.Token))
		}
		raw, err := base64.RawURLEncoding.DecodeString(invite.Token)
		if err != nil || len(raw) != 32 {
			t.Fatalf("邀请令牌应解码为 32 字节随机值，实际 len=%d err=%v", len(raw), err)
		}
		if invite.TokenHash == "" || invite.TokenHash == invite.Token {
			t.Fatal("数据库应只保存令牌哈希")
		}
	}
	if editorInvite.Role != model.RoomRoleEditor || viewerInvite.Role != model.RoomRoleViewer {
		t.Fatalf("邀请角色错误: %v %v", editorInvite.Role, viewerInvite.Role)
	}
	if editorInvite.ExpiresAt.Before(time.Now()) {
		t.Fatal("默认有效期邀请不应立即过期")
	}

	// --- 反例：editor/viewer 不能创建邀请（仅 owner 可管成员） ---
	if _, err := CreateRoomInvite(ctxOutsider, room.ID, "editor", 0); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("非成员创建邀请应 ErrRoomNotFound，实际 %v", err)
	}

	// --- 正例：凭有效邀请加入，成为对应角色成员 ---
	joinedEditor, err := AcceptRoomInvite(ctxEditor, editorInvite.Token)
	if err != nil {
		t.Fatalf("editor 接受邀请失败: %v", err)
	}
	if joinedEditor.ID != room.ID || joinedEditor.Role != model.RoomRoleEditor {
		t.Fatalf("editor 加入结果错误: %+v", joinedEditor)
	}
	joinedViewer, err := AcceptRoomInvite(ctxViewer, viewerInvite.Token)
	if err != nil {
		t.Fatalf("viewer 接受邀请失败: %v", err)
	}
	if joinedViewer.Role != model.RoomRoleViewer {
		t.Fatalf("viewer 加入角色错误: %v", joinedViewer.Role)
	}

	// --- 角色语义：editor 可写，viewer 只读，仅 owner 可管成员 ---
	if _, err := RequireRoomWriter(ctxEditor, room.ID); err != nil {
		t.Fatalf("editor 应可写，实际 %v", err)
	}
	if _, err := RequireRoomWriter(ctxViewer, room.ID); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("viewer 写操作应 ErrRoomForbidden，实际 %v", err)
	}
	if _, err := RequireRoomOwner(ctxEditor, room.ID); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("editor 的 owner 专属操作应 ErrRoomForbidden，实际 %v", err)
	}
	if _, err := RequireRoomOwner(ctxViewer, room.ID); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("viewer 的 owner 专属操作应 ErrRoomForbidden，实际 %v", err)
	}
	if _, err := CreateRoomInvite(ctxEditor, room.ID, "viewer", 0); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("editor 创建邀请应 ErrRoomForbidden，实际 %v", err)
	}
	if _, err := CreateRoomInvite(ctxViewer, room.ID, "viewer", 0); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("viewer 创建邀请应 ErrRoomForbidden，实际 %v", err)
	}

	// --- 成员列表：任何成员可读，非成员 not_found，且补全公开信息 ---
	members, err := RoomMembers(ctxViewer, room.ID)
	if err != nil {
		t.Fatalf("viewer 读取成员列表失败: %v", err)
	}
	if members.Total != 3 || roleOf(t, members, ownerID) != model.RoomRoleOwner ||
		roleOf(t, members, editorID) != model.RoomRoleEditor || roleOf(t, members, viewerID) != model.RoomRoleViewer {
		t.Fatalf("成员列表内容错误: %+v", members.Items)
	}
	for _, item := range members.Items {
		if item.Username == "" {
			t.Fatalf("成员列表应补全用户名: %+v", item)
		}
	}
	if _, err := RoomMembers(ctxOutsider, room.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("非成员读取成员列表应 ErrRoomNotFound，实际 %v", err)
	}

	// --- 反例：无效令牌不能加入，也不创建成员关系 ---
	if _, err := AcceptRoomInvite(ctxOutsider, "not-a-real-token"); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("无效令牌应 ErrInviteInvalid，实际 %v", err)
	}
	if _, err := AcceptRoomInvite(ctxOutsider, ""); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("空令牌应 ErrInviteInvalid，实际 %v", err)
	}
	if _, err := CurrentUserRoom(ctxOutsider, room.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("无效令牌不应创建成员关系，实际 %v", err)
	}

	// --- 反例：过期邀请被拒（直接写入一条已过期记录） ---
	expiredToken := "expired-token-for-test-only"
	if err := repository.CreateRoomInvite(model.RoomInvite{
		ID: newID("invite-expired"), RoomID: room.ID, Role: model.RoomRoleViewer,
		TokenHash: hashInviteToken(expiredToken), CreatedBy: ownerID,
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("写入过期邀请失败: %v", err)
	}
	if _, err := AcceptRoomInvite(ctxOutsider, expiredToken); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("过期邀请应 ErrInviteInvalid，实际 %v", err)
	}

	// --- 场景一：令牌被撤销但成员仍存在 ---
	// editor 已通过 editorInvite 成为成员；撤销该邀请后 editor 仍是成员。
	if err := RevokeRoomInvite(ctxOwner, room.ID, editorInvite.ID); err != nil {
		t.Fatalf("撤销 editor 邀请失败: %v", err)
	}
	if _, err := CurrentUserRoom(ctxEditor, room.ID); err != nil {
		t.Fatalf("撤销邀请不应移除既有成员: %v", err)
	}
	if _, err := RequireRoomWriter(ctxEditor, room.ID); err != nil {
		t.Fatalf("撤销邀请不应影响既有 editor 权限: %v", err)
	}
	// 旧令牌立即失效：非成员使用被撤销的令牌被拒。
	if _, err := AcceptRoomInvite(ctxOutsider, editorInvite.Token); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("被撤销邀请应 ErrInviteInvalid，实际 %v", err)
	}
	if _, err := CurrentUserRoom(ctxOutsider, room.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("被撤销邀请不应创建成员关系，实际 %v", err)
	}
	// 重复撤销幂等成功；撤销不存在邀请返回 not_found。
	if err := RevokeRoomInvite(ctxOwner, room.ID, editorInvite.ID); err != nil {
		t.Fatalf("重复撤销应幂等成功，实际 %v", err)
	}
	if err := RevokeRoomInvite(ctxOwner, room.ID, newID("invite-missing")); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("撤销不存在邀请应 ErrRoomNotFound，实际 %v", err)
	}

	// --- 反例：editor 不能撤销邀请 ---
	if err := RevokeRoomInvite(ctxEditor, room.ID, viewerInvite.ID); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("editor 撤销邀请应 ErrRoomForbidden，实际 %v", err)
	}

	// --- 反例：editor 不能移除成员、不能改角色 ---
	if err := RemoveRoomMember(ctxEditor, room.ID, viewerID); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("editor 移除成员应 ErrRoomForbidden，实际 %v", err)
	}
	if _, err := UpdateRoomMemberRole(ctxEditor, room.ID, viewerID, "editor"); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("editor 改角色应 ErrRoomForbidden，实际 %v", err)
	}

	// --- 反例：owner 不能移除自己/owner，不能改自己角色，不能授予 owner ---
	if err := RemoveRoomMember(ctxOwner, room.ID, ownerID); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("owner 移除自己应 ErrRoomForbidden，实际 %v", err)
	}
	if _, err := UpdateRoomMemberRole(ctxOwner, room.ID, ownerID, "viewer"); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("owner 改自己角色应 ErrRoomForbidden，实际 %v", err)
	}
	if _, err := UpdateRoomMemberRole(ctxOwner, room.ID, viewerID, "owner"); !errors.Is(err, ErrRoomInvalid) {
		t.Fatalf("授予 owner 应 ErrRoomInvalid，实际 %v", err)
	}
	if err := RemoveRoomMember(ctxOwner, room.ID, outsiderID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("移除非成员应 ErrRoomNotFound，实际 %v", err)
	}
	if _, err := UpdateRoomMemberRole(ctxOwner, room.ID, outsiderID, "viewer"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("改非成员角色应 ErrRoomNotFound，实际 %v", err)
	}

	// --- 角色升降级：editor→viewer 权限收紧，viewer→editor 放开 ---
	if _, err := UpdateRoomMemberRole(ctxOwner, room.ID, editorID, "viewer"); err != nil {
		t.Fatalf("owner 降级 editor 失败: %v", err)
	}
	if _, err := RequireRoomWriter(ctxEditor, room.ID); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("降级后 editor 写操作应被拒，实际 %v", err)
	}
	if _, err := UpdateRoomMemberRole(ctxOwner, room.ID, viewerID, "editor"); err != nil {
		t.Fatalf("owner 升级 viewer 失败: %v", err)
	}
	if _, err := RequireRoomWriter(ctxViewer, room.ID); err != nil {
		t.Fatalf("升级后 viewer 应可写，实际 %v", err)
	}

	// --- 场景二：成员被移除但旧链接仍有效 ---
	// viewerInvite 仍有效；先移除 viewer 成员，其立即失去访问；再用旧链接重新加入。
	if err := RemoveRoomMember(ctxOwner, room.ID, viewerID); err != nil {
		t.Fatalf("owner 移除 viewer 失败: %v", err)
	}
	if _, err := CurrentUserRoomMember(ctxViewer, room.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("被移除成员应失去访问，实际 %v", err)
	}
	if _, err := RoomMembers(ctxViewer, room.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("被移除成员不应再读成员列表，实际 %v", err)
	}
	rejoined, err := AcceptRoomInvite(ctxViewer, viewerInvite.Token)
	if err != nil {
		t.Fatalf("有效旧链接应仍可加入，实际 %v", err)
	}
	if rejoined.Role != model.RoomRoleViewer {
		t.Fatalf("旧链接重新加入角色应回到邀请角色 viewer，实际 %v", rejoined.Role)
	}

	// --- 令牌不外泄：邀请列表/成员列表不含令牌或哈希 ---
	invites, err := RoomInvites(ctxOwner, room.ID)
	if err != nil {
		t.Fatalf("owner 读取邀请列表失败: %v", err)
	}
	encoded, err := json.Marshal(invites)
	if err != nil {
		t.Fatalf("序列化邀请列表失败: %v", err)
	}
	for _, secret := range []string{viewerInvite.Token, viewerInvite.TokenHash, editorInvite.Token, editorInvite.TokenHash} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("邀请列表不应包含令牌或哈希")
		}
	}
	createdJSON, err := json.Marshal(viewerInvite)
	if err != nil {
		t.Fatalf("序列化创建结果失败: %v", err)
	}
	if !strings.Contains(string(createdJSON), viewerInvite.Token) {
		t.Fatal("创建邀请响应应返回明文令牌一次")
	}
	if strings.Contains(string(createdJSON), viewerInvite.TokenHash) {
		t.Fatal("创建邀请响应不应包含令牌哈希")
	}
	if _, err := RoomInvites(ctxEditor, room.ID); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("editor 读取邀请列表应 ErrRoomForbidden，实际 %v", err)
	}

	// --- 级联删除：删除房间后邀请记录一并清理，旧令牌不再可用 ---
	if err := DeleteCurrentUserRoom(ctxOwner, room.ID); err != nil {
		t.Fatalf("owner 删除房间失败: %v", err)
	}
	if _, ok, err := repository.FindRoomInviteByHash(viewerInvite.TokenHash); err != nil || ok {
		t.Fatalf("删除房间后邀请应被清理: ok=%v err=%v", ok, err)
	}
	if _, err := AcceptRoomInvite(ctxOutsider, viewerInvite.Token); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("房间删除后旧令牌应失效，实际 %v", err)
	}

	// --- 反例：未登录（context 无用户）时全部拒绝 ---
	if _, err := CurrentUserRoomMember(context.Background(), room.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("未登录读取成员关系应 ErrRoomNotFound，实际 %v", err)
	}
	if _, err := CreateRoomInvite(context.Background(), room.ID, "viewer", 0); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("未登录创建邀请应 ErrRoomNotFound，实际 %v", err)
	}
	if _, err := AcceptRoomInvite(context.Background(), viewerInvite.Token); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("未登录接受邀请应 ErrRoomNotFound，实际 %v", err)
	}
}
