package service

import (
	"context"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/internal/collab/realtime"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// WSTicketResult 是用 JWT 换取的短时效单次 WS 握手凭据。
type WSTicketResult struct {
	Ticket          string    `json:"ticket"`
	RoomID          string    `json:"roomId"`
	ProtocolVersion string    `json:"protocolVersion"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

// IssueRoomWSTicket 用当前登录用户的 JWT 换取一次性 ticket。
// 只有房间成员可换取；未登录/非成员统一返回 ErrRoomNotFound，不泄漏房间是否存在。
// JWT 本身绝不进入 URL，客户端拿到 ticket 后在首帧 hello 中携带。
func IssueRoomWSTicket(ctx context.Context, roomID string) (WSTicketResult, error) {
	user, ok := UserFromContext(ctx)
	if !ok || strings.TrimSpace(user.ID) == "" {
		return WSTicketResult{}, ErrRoomNotFound
	}
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return WSTicketResult{}, ErrRoomNotFound
	}
	if _, err := CurrentUserRoomMember(ctx, roomID); err != nil {
		return WSTicketResult{}, err
	}
	ticket, err := realtime.DefaultTickets.Issue(user.ID, user.Username, roomID, realtime.DefaultTicketTTL)
	if err != nil {
		return WSTicketResult{}, err
	}
	return WSTicketResult{
		Ticket:          ticket.Value,
		RoomID:          roomID,
		ProtocolVersion: contract.ProtocolVersion,
		ExpiresAt:       ticket.ExpiresAt,
	}, nil
}

// RealtimeResolver 返回实时层的数据访问适配器（成员角色与公共图快照）。
func RealtimeResolver() realtime.RoomResolver { return realtimeResolver{} }

type realtimeResolver struct{}

// ResolveMember 在建连时按当前成员表二次解析角色，成员被移除后即无法建连。
func (realtimeResolver) ResolveMember(roomID, userID string) (string, bool, error) {
	member, ok, err := repository.FindRoomMember(strings.TrimSpace(roomID), strings.TrimSpace(userID))
	if err != nil || !ok {
		return "", false, err
	}
	return string(member.Role), true, nil
}

// LoadSnapshot 读取房间公开信息与公共图（仅未删除节点/连线）。
func (realtimeResolver) LoadSnapshot(roomID string) (string, int64, []model.RoomNode, []model.RoomEdge, error) {
	roomID = strings.TrimSpace(roomID)
	room, ok, err := repository.FindRoom(roomID)
	if err != nil {
		return "", 0, nil, nil, err
	}
	if !ok {
		return "", 0, nil, nil, ErrRoomNotFound
	}
	nodes, edges, err := repository.ListRoomGraph(roomID)
	if err != nil {
		return "", 0, nil, nil, err
	}
	return room.Title, room.Revision, nodes, edges, nil
}

// broadcastRoomChanges 在 #8 事务提交成功后广播一次 changes。
// 幂等重放（Idempotent=true）不重复广播，与契约 4.4 一致。
func broadcastRoomChanges(roomID string, result model.RoomBatchResult) {
	if result.Idempotent {
		return
	}
	realtime.Default.BroadcastChanges(strings.TrimSpace(roomID), result)
}
