package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
)

const (
	// MaxConnectionsPerRoom 是单房间在线连接上限，超出返回 too_many_connections。
	MaxConnectionsPerRoom = 64
	// helloTimeout 是等待客户端首帧 hello 的时限。
	helloTimeout = 10 * time.Second
	// snapshotTimeout 是发送一次分帧快照的总时限。
	snapshotTimeout = 30 * time.Second
	// maxMessageBytes 是单帧上行负载上限（1 MiB）。
	maxMessageBytes = 1 << 20
	// pingInterval 是服务端心跳间隔，超时未通即判定掉线。
	pingInterval = 30 * time.Second
)

// RoomResolver 提供实时层所需的房间数据访问能力。由 service 层实现，
// 使本包不依赖 repository/service，避免与热点文件形成循环依赖。
type RoomResolver interface {
	// ResolveMember 返回用户在房间内的角色；非成员返回 ok=false。
	ResolveMember(roomID, userID string) (role string, ok bool, err error)
	// LoadSnapshot 返回房间标题、当前 revision 与公共图节点/连线行。
	LoadSnapshot(roomID string) (title string, revision int64, nodes []model.RoomNode, edges []model.RoomEdge, err error)
}

// Serve 处理一次 WS 建连：Accept → 首帧 hello 鉴权（ticket + 协议版本 + 成员）
// → 回发 hello 确认与分帧快照 → 进入读写循环。
//
// 鉴权失败时先发送 error 事件，再以 4401（未授权/协议不符）或 4403（非成员）关闭。
func Serve(w http.ResponseWriter, r *http.Request, roomID string, resolver RoomResolver) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// 身份来自显式 ticket 而非 Cookie，不受跨站 Cookie 影响，因此关闭 Origin 校验。
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxMessageBytes)

	hello, ok := readHello(conn)
	if !ok {
		return
	}
	if errCode, message := validateHello(hello, roomID); errCode != "" {
		closeWithError(conn, closeUnauthorized, errCode, message)
		return
	}

	userID, username, ticketRoom, ok := DefaultTickets.Consume(hello.Ticket)
	if !ok || ticketRoom != roomID {
		closeWithError(conn, closeUnauthorized, contract.CodeUnauthorized, "WS ticket 无效、已过期或已被使用")
		return
	}

	role, member, err := resolver.ResolveMember(roomID, userID)
	if err != nil {
		closeWithError(conn, websocket.StatusInternalError, contract.CodeUnauthorized, "成员校验失败")
		return
	}
	if !member {
		closeWithError(conn, closeForbidden, contract.CodeForbidden, "非房间成员")
		return
	}
	if Default.RoomConnectionCount(roomID) >= MaxConnectionsPerRoom {
		closeWithError(conn, closeTooMany, contract.CodeTooManyConnections, "房间在线连接已达上限")
		return
	}

	title, revision, nodes, edges, err := resolver.LoadSnapshot(roomID)
	if err != nil {
		closeWithError(conn, websocket.StatusInternalError, contract.CodeNotFound, "房间快照读取失败")
		return
	}

	client := &Client{
		hub:      Default,
		conn:     conn,
		roomID:   roomID,
		userID:   userID,
		username: username,
		role:     role,
		send:     make(chan []byte, sendBufferSize),
		done:     make(chan struct{}),
	}

	// 回发 hello 连接确认：协议版本、身份、角色与当前 revision。
	writeCtx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	err = client.sendNow(writeCtx, HelloEvent{
		Type:            EventHello,
		ProtocolVersion: contract.ProtocolVersion,
		RoomID:          roomID,
		UserID:          userID,
		Role:            role,
		Revision:        revision,
	})
	cancel()
	if err != nil {
		return
	}

	// 断线恢复：客户端 revision 与当前一致时无需重建，仅回一个空快照结束帧；
	// 落后或未带 revision 时全量分帧重建。
	needSnapshot := hello.Revision == nil || *hello.Revision != revision
	if err := sendSnapshot(client, userID, role, title, revision, nodes, edges, needSnapshot); err != nil {
		return
	}

	Default.register(client)
	go client.writeLoop()
	go client.pingLoop()
	client.readLoop()
	Default.unregister(client)
	client.close(websocket.StatusNormalClosure, "connection closed")
}

// readHello 读取并解析首帧 hello；超时、非文本或非法 JSON 均返回 false。
func readHello(conn *websocket.Conn) (ClientMessage, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), helloTimeout)
	defer cancel()
	var hello ClientMessage
	if err := wsjson.Read(ctx, conn, &hello); err != nil {
		return ClientMessage{}, false
	}
	return hello, true
}

// validateHello 校验首帧类型、协议版本与房间一致性，返回契约错误码与提示。
func validateHello(hello ClientMessage, roomID string) (string, string) {
	if hello.Type != ClientHello {
		return contract.CodeInvalidPayload, "首帧必须是 hello"
	}
	if hello.ProtocolVersion != contract.ProtocolVersion {
		return contract.CodeProtocolMismatch, "协议版本不匹配"
	}
	if strings.TrimSpace(hello.RoomID) != "" && hello.RoomID != roomID {
		return contract.CodeInvalidPayload, "hello 房间与连接房间不一致"
	}
	if strings.TrimSpace(hello.Ticket) == "" {
		return contract.CodeUnauthorized, "缺少 WS ticket"
	}
	return "", ""
}

// sendSnapshot 发送分帧快照。includeGraph 为 false 时只发开始/结束帧（客户端已最新）。
func sendSnapshot(c *Client, userID, role, title string, revision int64, nodes []model.RoomNode, edges []model.RoomEdge, includeGraph bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
	defer cancel()
	start := SnapshotStartEvent{
		Type:   EventSnapshotStart,
		UserID: userID,
		Room:   RoomInfo{ID: c.roomID, Title: title, Revision: revision, Role: role},
	}
	if err := c.sendNow(ctx, start); err != nil {
		return err
	}
	if includeGraph {
		for _, node := range nodes {
			event := SnapshotNodeEvent{Type: EventSnapshotNode, Node: NewNodeView(node), OwnPrivate: false}
			if err := c.sendNow(ctx, event); err != nil {
				return err
			}
		}
		for _, edge := range edges {
			event := SnapshotEdgeEvent{Type: EventSnapshotEdge, Edge: NewEdgeView(edge)}
			if err := c.sendNow(ctx, event); err != nil {
				return err
			}
		}
	}
	return c.sendNow(ctx, SnapshotEndEvent{Type: EventSnapshotEnd, Revision: revision})
}

// readLoop 读取客户端上行帧，只处理协作光标（写操作走 REST /operations 二次鉴权）。
func (c *Client) readLoop() {
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			return
		}
		var message ClientMessage
		if json.Unmarshal(data, &message) != nil {
			continue
		}
		switch message.Type {
		case ClientCursors:
			if message.Position == nil || !c.allowCursor() {
				continue
			}
			c.hub.BroadcastCursors(c.roomID, c.userID, c.username, *message.Position)
		default:
			// 重复 hello、未知事件一律忽略，避免客户端绕过 REST 二次鉴权写入。
		}
	}
}

// allowCursor 按 cursorThrottle 对单连接光标上行做节流。
func (c *Client) allowCursor() bool {
	now := time.Now().UnixNano()
	return c.lastCursor.compareAndStore(now-int64(cursorThrottle), now)
}

// pingLoop 周期性发送心跳，超时即关闭连接，触发掉线清理与在线状态刷新。
func (c *Client) pingLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.conn.Ping(ctx)
			cancel()
			if err != nil {
				c.close(websocket.StatusNormalClosure, "心跳超时")
				return
			}
		}
	}
}

// closeWithError 发送 error 事件后以指定关闭码关闭连接。
func closeWithError(conn *websocket.Conn, code websocket.StatusCode, errCode, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_ = wsjson.Write(ctx, conn, ErrorEvent{Type: EventError, Code: errCode, Message: message})
	cancel()
	_ = conn.Close(code, errCode)
}
