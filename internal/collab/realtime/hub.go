package realtime

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/tigerowo/infinite-canvas/model"
)

// sendBufferSize 是单连接发送队列长度；队列满时丢弃最旧语义的新消息（背压保护）。
const sendBufferSize = 64

// writeTimeout 是单帧写超时，避免慢客户端阻塞广播。
const writeTimeout = 10 * time.Second

// cursorThrottle 是单连接协作光标的最小上行间隔，节流合并避免刷屏。
const cursorThrottle = 50 * time.Millisecond

// 自定义关闭码：4401 未授权/会话失效，4403 非成员/无权限，4429 连接数超限。
const (
	closeUnauthorized websocket.StatusCode = 4401
	closeForbidden    websocket.StatusCode = 4403
	closeTooMany      websocket.StatusCode = 4429
)

// Client 是一条已鉴权的房间 WS 连接。
type Client struct {
	hub      *Hub
	conn     *websocket.Conn
	roomID   string
	userID   string
	username string
	role     string

	send chan []byte
	done chan struct{}

	closeOnce   sync.Once
	closeCode   websocket.StatusCode
	closeReason string

	lastCursor atomicInt64
}

// Hub 维护所有房间的在线连接，负责广播、在线状态与强制断开。
type Hub struct {
	mu    sync.Mutex
	rooms map[string]map[*Client]struct{}
}

// NewHub 创建空 Hub。
func NewHub() *Hub {
	return &Hub{rooms: make(map[string]map[*Client]struct{})}
}

// Default 是进程级 Hub。
var Default = NewHub()

// register 把连接加入房间并广播最新在线成员。
func (h *Hub) register(c *Client) {
	h.mu.Lock()
	room := h.rooms[c.roomID]
	if room == nil {
		room = make(map[*Client]struct{})
		h.rooms[c.roomID] = room
	}
	room[c] = struct{}{}
	h.broadcastPresenceLocked(c.roomID)
	h.mu.Unlock()
}

// unregister 移除连接并广播最新在线成员。幂等。
func (h *Hub) unregister(c *Client) {
	h.mu.Lock()
	room := h.rooms[c.roomID]
	if room == nil {
		h.mu.Unlock()
		return
	}
	if _, ok := room[c]; !ok {
		h.mu.Unlock()
		return
	}
	delete(room, c)
	if len(room) == 0 {
		delete(h.rooms, c.roomID)
	} else {
		h.broadcastPresenceLocked(c.roomID)
	}
	h.mu.Unlock()
}

// RoomConnectionCount 返回房间当前连接数（含同用户多连接）。
func (h *Hub) RoomConnectionCount(roomID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.rooms[roomID])
}

// BroadcastChanges 在 #8 事务提交成功后广播一次 changes。幂等重放不应调用本方法。
func (h *Hub) BroadcastChanges(roomID string, result model.RoomBatchResult) {
	event := ChangesEvent{
		Type:        EventChanges,
		OperationID: result.OperationID,
		Revision:    result.Revision,
		Changes:     ChangeViews(result.Changes),
	}
	h.broadcast(roomID, event)
}

// BroadcastCursors 把一条协作光标节流合并后广播给同房间其他连接（不回显给发送者）。
func (h *Hub) BroadcastCursors(roomID, userID, username string, position Position) {
	event := CursorsEvent{
		Type:    EventCursors,
		Cursors: []CursorInfo{{UserID: userID, Username: username, Position: position}},
	}
	h.broadcastExceptUser(roomID, userID, event)
}

// DisconnectUser 关闭某用户在某房间的全部连接（成员移除、角色降级、登出）。
func (h *Hub) DisconnectUser(roomID, userID, reason string) {
	h.mu.Lock()
	var targets []*Client
	for c := range h.rooms[roomID] {
		if c.userID == userID {
			targets = append(targets, c)
		}
	}
	h.mu.Unlock()
	for _, c := range targets {
		c.close(closeUnauthorized, reason)
	}
}

// DisconnectUserEverywhere 关闭某用户在全部房间的连接（登出、封禁）。
func (h *Hub) DisconnectUserEverywhere(userID, reason string) {
	h.mu.Lock()
	var targets []*Client
	for _, room := range h.rooms {
		for c := range room {
			if c.userID == userID {
				targets = append(targets, c)
			}
		}
	}
	h.mu.Unlock()
	for _, c := range targets {
		c.close(closeUnauthorized, reason)
	}
}

// DisconnectRoom 关闭房间内全部连接（房间被删除）。
func (h *Hub) DisconnectRoom(roomID, reason string) {
	h.mu.Lock()
	var targets []*Client
	for c := range h.rooms[roomID] {
		targets = append(targets, c)
	}
	h.mu.Unlock()
	for _, c := range targets {
		c.close(closeUnauthorized, reason)
	}
}

// broadcast 向房间全部连接入队一条事件。
func (h *Hub) broadcast(roomID string, event any) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.mu.Lock()
	for c := range h.rooms[roomID] {
		c.enqueue(payload)
	}
	h.mu.Unlock()
}

// broadcastExceptUser 向房间内除指定用户外的连接入队一条事件。
func (h *Hub) broadcastExceptUser(roomID, exceptUserID string, event any) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.mu.Lock()
	for c := range h.rooms[roomID] {
		if c.userID == exceptUserID {
			continue
		}
		c.enqueue(payload)
	}
	h.mu.Unlock()
}

// broadcastPresenceLocked 计算并广播房间在线成员（按 userId 去重）。调用方需持有锁。
func (h *Hub) broadcastPresenceLocked(roomID string) {
	seen := make(map[string]string)
	for c := range h.rooms[roomID] {
		if _, ok := seen[c.userID]; !ok {
			seen[c.userID] = c.username
		}
	}
	members := make([]MemberInfo, 0, len(seen))
	for userID, username := range seen {
		members = append(members, MemberInfo{UserID: userID, Username: username})
	}
	payload, err := json.Marshal(PresenceEvent{Type: EventPresence, Count: len(members), Members: members})
	if err != nil {
		return
	}
	for c := range h.rooms[roomID] {
		c.enqueue(payload)
	}
}

// enqueue 非阻塞入队；队列满或连接已关闭时丢弃，绝不阻塞广播方。
func (c *Client) enqueue(payload []byte) {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.send <- payload:
	case <-c.done:
	default:
	}
}

// close 幂等地标记连接关闭，由写循环负责发送关闭帧。
func (c *Client) close(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		c.closeCode = code
		c.closeReason = reason
		close(c.done)
	})
}

// writeLoop 是唯一的写协程：串行写帧，收到关闭信号后发送关闭帧退出。
func (c *Client) writeLoop() {
	for {
		select {
		case <-c.done:
			_ = c.conn.Close(c.closeCode, c.closeReason)
			return
		case payload := <-c.send:
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.conn.Write(ctx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				c.close(websocket.StatusNormalClosure, "write failed")
				return
			}
		}
	}
}

// sendNow 直接写一帧（仅用于建连阶段、写循环启动前）。
func (c *Client) sendNow(ctx context.Context, event any) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return c.conn.Write(ctx, websocket.MessageText, payload)
}

// atomicInt64 是一个无锁 int64，用于光标节流时间戳。
type atomicInt64 struct {
	mu  sync.Mutex
	val int64
}

func (a *atomicInt64) load() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.val
}

func (a *atomicInt64) store(value int64) {
	a.mu.Lock()
	a.val = value
	a.mu.Unlock()
}

// compareAndStore 若当前值早于 threshold 则写入 value 并返回 true（表示允许通过）。
func (a *atomicInt64) compareAndStore(threshold, value int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.val > threshold {
		return false
	}
	a.val = value
	return true
}
