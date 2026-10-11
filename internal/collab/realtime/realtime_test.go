package realtime

import (
	"context"
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
