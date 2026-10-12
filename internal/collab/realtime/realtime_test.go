package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/tigerowo/infinite-canvas/model"
)

type stubResolver struct{}

func (stubResolver) ResolveMember(roomID, userID string) (string, bool, error) {
	return "editor", true, nil
}

func (stubResolver) LoadSnapshot(roomID string) (string, int64, []model.RoomNode, []model.RoomEdge, error) {
	nodes := []model.RoomNode{{
		RoomID: roomID, ID: "node_unit_001", Kind: "image", Title: "节点",
		PositionX: 1, PositionY: 2, Width: 10, Height: 10, Version: 1,
		Metadata: `{"prompt":"公开","imageTaskId":"secret","apiKey":"sk-x","status":"running"}`,
	}}
	return "单元房间", 7, nodes, nil, nil
}

// TestTicketSingleUseAndExpiry 校验 ticket 一次性、绑定房间且过期失效。
func TestTicketSingleUseAndExpiry(t *testing.T) {
	store := NewTicketStore()
	ticket, err := store.Issue("user_1", "alice", "room_1", time.Minute)
	if err != nil {
		t.Fatalf("签发 ticket 失败: %v", err)
	}
	userID, username, roomID, ok := store.Consume(ticket.Value)
	if !ok || userID != "user_1" || username != "alice" || roomID != "room_1" {
		t.Fatalf("首次消费应成功并返回绑定身份，实际 ok=%v user=%q room=%q", ok, userID, roomID)
	}
	if _, _, _, ok := store.Consume(ticket.Value); ok {
		t.Fatal("ticket 只能使用一次，二次消费应失败")
	}

	expired, err := store.Issue("user_2", "bob", "room_1", time.Millisecond)
	if err != nil {
		t.Fatalf("签发过期 ticket 失败: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, _, _, ok := store.Consume(expired.Value); ok {
		t.Fatal("过期 ticket 不应可用")
	}
}

// TestNodeViewRedactsPrivateMetadata 校验广播视图剥离 private/local 与凭据字段。
func TestNodeViewRedactsPrivateMetadata(t *testing.T) {
	view := NewNodeView(model.RoomNode{
		ID: "n1", Kind: "image", Metadata: `{"prompt":"p","imageTaskId":"t","apiKey":"k","status":"running","progress":50}`,
	})
	if view.Metadata["prompt"] != "p" {
		t.Fatalf("public 字段应保留，实际 %v", view.Metadata)
	}
	for _, forbidden := range []string{"imageTaskId", "apiKey", "status", "progress"} {
		if _, ok := view.Metadata[forbidden]; ok {
			t.Fatalf("禁止字段 %q 不应出现在广播视图", forbidden)
		}
	}
}

// TestServeHandshakeAndSnapshot 在标准 net/http 上验证握手、hello 确认与分帧快照。
func TestServeHandshakeAndSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Serve(w, r, "room_unit_001", stubResolver{})
	}))
	defer server.Close()

	ticket, err := DefaultTickets.Issue("user_unit", "alice", "room_unit_001", time.Minute)
	if err != nil {
		t.Fatalf("签发 ticket 失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("建立连接失败: %v", err)
	}
	defer conn.CloseNow()
	if err := wsjson.Write(ctx, conn, map[string]any{
		"type": "hello", "protocolVersion": "1", "roomId": "room_unit_001", "ticket": ticket.Value,
	}); err != nil {
		t.Fatalf("发送 hello 失败: %v", err)
	}

	types := map[string]bool{}
	var nodeEvent map[string]any
	for {
		var event map[string]any
		if err := wsjson.Read(ctx, conn, &event); err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		eventType, _ := event["type"].(string)
		types[eventType] = true
		if eventType == "snapshot-node" {
			nodeEvent = event
		}
		if eventType == "snapshot-end" {
			break
		}
	}
	for _, want := range []string{"hello", "snapshot-start", "snapshot-node", "snapshot-end"} {
		if !types[want] {
			t.Fatalf("缺少事件 %s，实际收到 %v", want, types)
		}
	}
	node, _ := nodeEvent["node"].(map[string]any)
	metadata, _ := node["metadata"].(map[string]any)
	if _, ok := metadata["apiKey"]; ok {
		t.Fatalf("快照节点泄漏 apiKey: %v", metadata)
	}
}

// TestProtocolMismatchCloses4401 协议版本不匹配时以 4401 关闭。
func TestProtocolMismatchCloses4401(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Serve(w, r, "room_unit_001", stubResolver{})
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("建立连接失败: %v", err)
	}
	defer conn.CloseNow()
	if err := wsjson.Write(ctx, conn, map[string]any{
		"type": "hello", "protocolVersion": "2", "roomId": "room_unit_001", "ticket": "x",
	}); err != nil {
		t.Fatalf("发送 hello 失败: %v", err)
	}
	var closeCode websocket.StatusCode
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			closeCode = websocket.CloseStatus(err)
			break
		}
	}
	if closeCode != 4401 {
		t.Fatalf("协议不匹配关闭码应为 4401，实际 %v", closeCode)
	}
}

// blockingResolver 让 LoadSnapshot 阻塞在 entered/release 之间，
// 用于在「已注册、尚未取到快照」的窗口内注入 changes，复现 PR #47 P1 竞态。
type blockingResolver struct {
	entered  chan struct{}
	release  chan struct{}
	revision int64
	nodes    []model.RoomNode
	edges    []model.RoomEdge
}

func (r *blockingResolver) ResolveMember(roomID, userID string) (string, bool, error) {
	return "editor", true, nil
}

func (r *blockingResolver) LoadSnapshot(roomID string) (string, int64, []model.RoomNode, []model.RoomEdge, error) {
	close(r.entered)
	<-r.release
	return "并发房间", r.revision, r.nodes, r.edges, nil
}

// dialWS 建立 WS 连接并发送首帧 hello（revision 可选）。
func dialWS(t *testing.T, serverURL, roomID string, hello map[string]any) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("建立连接失败: %v", err)
	}
	if err := wsjson.Write(ctx, conn, hello); err != nil {
		conn.CloseNow()
		t.Fatalf("发送 hello 失败: %v", err)
	}
	return conn
}

// TestSnapshotSubscriptionAtomicBoundary 复现并验证 PR #47 P1 丢同步：
// 在「LoadSnapshot 取 revision」到「快照发完」之间注入一次 changes，客户端最终必须收到它。
func TestSnapshotSubscriptionAtomicBoundary(t *testing.T) {
	roomID := "room_snapshot_race_001"
	resolver := &blockingResolver{
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		revision: 7,
		nodes:    []model.RoomNode{{RoomID: roomID, ID: "n1", Kind: "text", Title: "旧节点", Version: 1}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Serve(w, r, roomID, resolver)
	}))
	defer server.Close()

	ticket, err := DefaultTickets.Issue("user_race", "alice", roomID, time.Minute)
	if err != nil {
		t.Fatalf("签发 ticket 失败: %v", err)
	}
	conn := dialWS(t, server.URL, roomID, map[string]any{
		"type": "hello", "protocolVersion": "1", "roomId": roomID, "ticket": ticket.Value,
	})
	defer conn.CloseNow()

	// 等到 LoadSnapshot 已进入：此刻连接已 registerPending（订阅态），但快照尚未返回。
	<-resolver.entered

	// 在快照窗口内注入一次 revision=8 的 changes（> 快照 revision 7）。
	Default.BroadcastChanges(roomID, model.RoomBatchResult{
		OperationID: "op_race",
		Revision:    8,
		Changes: []model.RoomChange{{
			Type: "upsert",
			Node: &model.RoomNode{RoomID: roomID, ID: "n2", Kind: "text", Title: "并发新增", Version: 1},
		}},
	})
	close(resolver.release)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var sawSnapshotEnd, sawChange bool
	var changeRev float64
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !(sawSnapshotEnd && sawChange) {
		var event map[string]any
		if err := wsjson.Read(ctx, conn, &event); err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		switch event["type"] {
		case "snapshot-end":
			sawSnapshotEnd = true
		case "changes":
			sawChange = true
			changeRev, _ = event["revision"].(float64)
		}
	}
	if !sawSnapshotEnd {
		t.Fatal("未收到 snapshot-end")
	}
	if !sawChange {
		t.Fatal("快照期间注入的 changes 被丢弃（PR #47 P1 丢同步）")
	}
	if changeRev != 8 {
		t.Fatalf("补发的 changes.revision 应为 8，实际 %v", changeRev)
	}
}

// TestSnapshotBoundaryDeliversChangesWhenClientUpToDate 客户端 revision 已最新时只发空快照，
// 但快照窗口内产生的 changes 仍必须补发，避免停在旧状态。
func TestSnapshotBoundaryDeliversChangesWhenClientUpToDate(t *testing.T) {
	roomID := "room_snapshot_uptodate_001"
	resolver := &blockingResolver{
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		revision: 7,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Serve(w, r, roomID, resolver)
	}))
	defer server.Close()

	ticket, err := DefaultTickets.Issue("user_uptodate", "bob", roomID, time.Minute)
	if err != nil {
		t.Fatalf("签发 ticket 失败: %v", err)
	}
	conn := dialWS(t, server.URL, roomID, map[string]any{
		"type": "hello", "protocolVersion": "1", "roomId": roomID, "ticket": ticket.Value, "revision": 7,
	})
	defer conn.CloseNow()

	<-resolver.entered
	Default.BroadcastChanges(roomID, model.RoomBatchResult{
		OperationID: "op_uptodate",
		Revision:    8,
		Changes: []model.RoomChange{{
			Type: "upsert",
			Node: &model.RoomNode{RoomID: roomID, ID: "n9", Kind: "text", Title: "窗口新增", Version: 1},
		}},
	})
	close(resolver.release)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sawChange := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !sawChange {
		var event map[string]any
		if err := wsjson.Read(ctx, conn, &event); err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		if event["type"] == "changes" {
			sawChange = true
			if rev, _ := event["revision"].(float64); rev != 8 {
				t.Fatalf("changes.revision 应为 8，实际 %v", rev)
			}
		}
	}
	if !sawChange {
		t.Fatal("客户端 revision 已最新时空快照窗口内的 changes 仍应补发")
	}
}

// TestActivateFiltersChangesAlreadyInSnapshot 验证原子边界过滤：revision <= 快照的 changes 丢弃，
// revision > 快照的补发，非 changes 事件一律补发。
func TestActivateFiltersChangesAlreadyInSnapshot(t *testing.T) {
	h := NewHub()
	c := &Client{
		hub: h, roomID: "room_filter_001", userID: "u1", username: "u",
		send: make(chan []byte, 8), done: make(chan struct{}),
	}
	h.registerPending(c)
	h.mu.Lock()
	h.deliverLocked(c, []byte(`{"type":"changes","revision":6}`), 6, true)
	h.deliverLocked(c, []byte(`{"type":"changes","revision":7}`), 7, true)
	h.deliverLocked(c, []byte(`{"type":"changes","revision":8}`), 8, true)
	h.mu.Unlock()
	h.activate(c, 7)

	var revs []int64
	for {
		select {
		case payload := <-c.send:
			var ev struct {
				Type     string `json:"type"`
				Revision int64  `json:"revision"`
			}
			if err := json.Unmarshal(payload, &ev); err != nil {
				t.Fatalf("解析补发事件失败: %v", err)
			}
			if ev.Type == "changes" {
				revs = append(revs, ev.Revision)
			}
		default:
			goto drained
		}
	}
drained:
	if len(revs) != 1 || revs[0] != 8 {
		t.Fatalf("应只补发 revision>快照 的 changes(8)，实际 %v", revs)
	}
	if c.syncing {
		t.Fatal("activate 后应退出快照订阅态")
	}
}

// TestEnqueueOverflowTriggersResync 验证发送队列溢出时显式降级为断线重连，绝不静默丢弃。
func TestEnqueueOverflowTriggersResync(t *testing.T) {
	c := &Client{send: make(chan []byte, 1), done: make(chan struct{})}
	c.enqueue([]byte("first"))
	c.enqueue([]byte("overflow"))
	select {
	case <-c.done:
	default:
		t.Fatal("发送队列溢出应显式关闭连接，而不是静默丢弃 changes")
	}
	if c.closeCode != closeResyncRequired {
		t.Fatalf("溢出关闭码应为 %d，实际 %d", closeResyncRequired, c.closeCode)
	}
}

// TestSlowClientOverflowDisconnects 慢客户端（不读取）导致发送队列溢出时，服务端必须显式断开，
// 而不是静默丢弃 changes 让客户端停在旧状态。
func TestSlowClientOverflowDisconnects(t *testing.T) {
	roomID := "room_slow_client_001"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Serve(w, r, roomID, stubResolver{})
	}))
	defer server.Close()

	ticket, err := DefaultTickets.Issue("user_slow", "carol", roomID, time.Minute)
	if err != nil {
		t.Fatalf("签发 ticket 失败: %v", err)
	}
	conn := dialWS(t, server.URL, roomID, map[string]any{
		"type": "hello", "protocolVersion": "1", "roomId": roomID, "ticket": ticket.Value,
	})
	defer conn.CloseNow()

	// 读到 snapshot-end，确认连接已 activate；之后不再读取，制造慢客户端。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		var event map[string]any
		if err := wsjson.Read(ctx, conn, &event); err != nil {
			t.Fatalf("读取快照失败: %v", err)
		}
		if event["type"] == "snapshot-end" {
			break
		}
	}

	node := model.RoomNode{RoomID: roomID, ID: "n_big", Kind: "text", Title: strings.Repeat("x", 8192), Version: 1}
	rev := int64(8)
	// 上限刻意小于 writeTimeout(10s)：旧实现静默丢弃时连接要到写超时才断，会超过该上限而失败；
	// 修复后队列溢出立即显式断开，远快于上限。
	overflowDeadline := time.Now().Add(5 * time.Second)
	for Default.RoomConnectionCount(roomID) != 0 && time.Now().Before(overflowDeadline) {
		Default.BroadcastChanges(roomID, model.RoomBatchResult{
			OperationID: fmt.Sprintf("op_slow_%d", rev),
			Revision:    rev,
			Changes:     []model.RoomChange{{Type: "upsert", Node: &node}},
		})
		rev++
	}
	if Default.RoomConnectionCount(roomID) != 0 {
		t.Fatalf("慢客户端发送队列溢出后连接未被显式断开（疑似静默丢弃 changes），已广播 %d 次", rev-8)
	}

	// 连接确实已断开：持续读取直到返回错误。
	readDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(readDeadline) {
		rctx, rcancel := context.WithTimeout(context.Background(), time.Second)
		_, _, err := conn.Read(rctx)
		rcancel()
		if err != nil {
			return
		}
	}
	t.Fatal("慢客户端连接应已被显式断开")
}
