package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gin-gonic/gin"
	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
	"github.com/tigerowo/infinite-canvas/service"
)

// wsTestEnv 是 WS 集成测试的共享环境：一个内存库 + 一个真实 HTTP/WS 服务。
type wsTestEnv struct {
	server   *httptest.Server
	engine   *gin.Engine
	roomID   string
	owner    model.AuthSession
	editor   model.AuthSession
	viewer   model.AuthSession
	outsider model.AuthSession
}

// setupWSTestEnv 初始化内存库、用户、房间与 HTTP 服务。
func setupWSTestEnv(t *testing.T) *wsTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	config.Cfg = config.Config{
		StorageDriver: "sqlite",
		DatabaseDSN:   ":memory:",
		JWTSecret:     "c01-05-test-secret",
		AILogDir:      t.TempDir(),
	}
	db, err := repository.DB()
	if err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	owner := registerWSUser(t, "owner_user")
	editor := registerWSUser(t, "editor_user")
	viewer := registerWSUser(t, "viewer_user")
	outsider := registerWSUser(t, "outsider_user")

	room, err := service.CreateRoom(wsUserContext(owner.User.ID), "WS 集成测试房间")
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	addWSMember(t, room.ID, editor.User.ID, model.RoomRoleEditor)
	addWSMember(t, room.ID, viewer.User.ID, model.RoomRoleViewer)

	engine := New()
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	return &wsTestEnv{
		server: server, engine: engine, roomID: room.ID,
		owner: owner, editor: editor, viewer: viewer, outsider: outsider,
	}
}

// wsUserSeq 让每个测试用唯一用户名：repository.DB() 是进程级单例，
// 同包多个测试共享同一个 :memory: 库。
var wsUserSeq int64

func registerWSUser(t *testing.T, base string) model.AuthSession {
	t.Helper()
	username := fmt.Sprintf("%s_%d", base, atomic.AddInt64(&wsUserSeq, 1))
	session, err := service.Register(username, "pw-12345678")
	if err != nil {
		t.Fatalf("注册测试用户 %s 失败: %v", username, err)
	}
	return session
}

func addWSMember(t *testing.T, roomID, userID string, role model.RoomMemberRole) {
	t.Helper()
	now := time.Now().UTC()
	if err := repository.SaveRoomMember(model.RoomMember{
		RoomID: roomID, UserID: userID, Role: role, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入成员失败: %v", err)
	}
}

func wsUserContext(userID string) context.Context {
	return service.WithUser(context.Background(), model.AuthUser{ID: userID})
}

// issueWSTicket 通过 REST 用 JWT 换 ticket。
func issueWSTicket(t *testing.T, env *wsTestEnv, token, roomID string) (int, string) {
	t.Helper()
	body := strings.NewReader("")
	req, err := http.NewRequest(http.MethodPost,
		env.server.URL+"/api/v1/collab/rooms/"+roomID+"/ws-ticket", body)
	if err != nil {
		t.Fatalf("构造 ticket 请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := env.server.Client().Do(req)
	if err != nil {
		t.Fatalf("请求 ticket 失败: %v", err)
	}
	defer resp.Body.Close()
	var payload struct {
		Code int `json:"code"`
		Data struct {
			Ticket string `json:"ticket"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	return resp.StatusCode, payload.Data.Ticket
}

// dialRoomWS 建立 WS 连接并完成首帧 hello 握手。
func dialRoomWS(t *testing.T, env *wsTestEnv, roomID, ticket string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(env.server.URL, "http") + "/api/v1/collab/rooms/" + roomID + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("建立 WS 连接失败: %v", err)
	}
	if err := wsjson.Write(ctx, conn, map[string]any{
		"type": "hello", "protocolVersion": "1", "roomId": roomID, "ticket": ticket,
	}); err != nil {
		t.Fatalf("发送 hello 失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	return conn
}

// waitForType 持续读取事件直到出现指定类型，忽略其他事件；超时返回 nil。
func waitForType(t *testing.T, conn *websocket.Conn, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var event map[string]any
		err := wsjson.Read(ctx, conn, &event)
		cancel()
		if err != nil {
			return nil
		}
		if event["type"] == want {
			return event
		}
	}
	return nil
}

// readUntilClosed 持续读取直到连接关闭，返回关闭码与错误。用于验证服务端主动断开。
func readUntilClosed(t *testing.T, conn *websocket.Conn) (websocket.StatusCode, error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _, err := conn.Read(ctx)
		cancel()
		if err != nil {
			return websocket.CloseStatus(err), err
		}
	}
	return 0, nil
}

// applyNodeCreate 以 owner 身份通过 REST 提交一次 create 操作。
func applyNodeCreate(t *testing.T, env *wsTestEnv, token, roomID, nodeID string) {
	t.Helper()
	envelope := map[string]any{
		"protocolVersion": "1",
		"roomId":          roomID,
		"operationId":     "op_" + nodeID,
		"operations": []map[string]any{{
			"type": "create",
			"node": map[string]any{
				"id": nodeID, "kind": "text", "title": "节点", "version": 0,
				"position": map[string]any{"x": 0, "y": 0}, "width": 100, "height": 100,
				"metadata": map[string]any{"content": "hello"},
			},
		}},
	}
	raw, _ := json.Marshal(envelope)
	req, err := http.NewRequest(http.MethodPost,
		env.server.URL+"/api/v1/collab/rooms/"+roomID+"/operations", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("构造 operations 请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := env.server.Client().Do(req)
	if err != nil {
		t.Fatalf("提交 operations 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner 提交 create 应 200，实际 %d", resp.StatusCode)
	}
}

// postOperations 以指定 token 提交一次 create，返回 HTTP 状态码。
func postOperations(t *testing.T, env *wsTestEnv, token, roomID, nodeID string) int {
	t.Helper()
	envelope := map[string]any{
		"protocolVersion": "1", "roomId": roomID, "operationId": "op_viewer_" + nodeID,
		"operations": []map[string]any{{
			"type": "create",
			"node": map[string]any{
				"id": nodeID, "kind": "text", "title": "越权节点", "version": 0,
				"position": map[string]any{"x": 1, "y": 1}, "width": 50, "height": 50,
				"metadata": map[string]any{"content": "x"},
			},
		}},
	}
	raw, _ := json.Marshal(envelope)
	req, _ := http.NewRequest(http.MethodPost,
		env.server.URL+"/api/v1/collab/rooms/"+roomID+"/operations", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := env.server.Client().Do(req)
	if err != nil {
		t.Fatalf("提交 operations 失败: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestWSChangesBroadcastToTwoClients 两个客户端连接同一房间，owner 提交后双方都收到 changes。
func TestWSChangesBroadcastToTwoClients(t *testing.T) {
	env := setupWSTestEnv(t)

	_, ownerTicket := issueWSTicket(t, env, env.owner.Token, env.roomID)
	ownerConn := dialRoomWS(t, env, env.roomID, ownerTicket)
	if end := waitForType(t, ownerConn, "snapshot-end"); end == nil {
		t.Fatal("owner 未收到 snapshot-end")
	}

	_, editorTicket := issueWSTicket(t, env, env.editor.Token, env.roomID)
	editorConn := dialRoomWS(t, env, env.roomID, editorTicket)
	if end := waitForType(t, editorConn, "snapshot-end"); end == nil {
		t.Fatal("editor 未收到 snapshot-end")
	}

	applyNodeCreate(t, env, env.owner.Token, env.roomID, "node_ws_001")

	ownerChanges := waitForType(t, ownerConn, "changes")
	if ownerChanges == nil {
		t.Fatal("owner 未收到 changes 广播")
	}
	editorChanges := waitForType(t, editorConn, "changes")
	if editorChanges == nil {
		t.Fatal("editor 未收到 changes 广播")
	}
	if rev, _ := ownerChanges["revision"].(float64); rev < 1 {
		t.Fatalf("changes.revision 应为正数，实际 %v", ownerChanges["revision"])
	}
	if fmt.Sprint(ownerChanges["operationId"]) != "op_node_ws_001" {
		t.Fatalf("changes.operationId 不匹配: %v", ownerChanges["operationId"])
	}
}

// TestWSViewerCanConnectButCannotWrite viewer 可建立 WS 连接，但写操作被拒。
func TestWSViewerCanConnectButCannotWrite(t *testing.T) {
	env := setupWSTestEnv(t)

	_, viewerTicket := issueWSTicket(t, env, env.viewer.Token, env.roomID)
	viewerConn := dialRoomWS(t, env, env.roomID, viewerTicket)
	hello := waitForType(t, viewerConn, "hello")
	if hello == nil {
		t.Fatal("viewer 应能建立 WS 连接并收到 hello")
	}
	if hello["role"] != "viewer" {
		t.Fatalf("viewer hello.role 应为 viewer，实际 %v", hello["role"])
	}
	if end := waitForType(t, viewerConn, "snapshot-end"); end == nil {
		t.Fatal("viewer 未收到 snapshot-end")
	}

	status := postOperations(t, env, env.viewer.Token, env.roomID, "node_viewer_001")
	if status != http.StatusForbidden {
		t.Fatalf("viewer 写操作应 403，实际 %d", status)
	}
}

// TestWSNonMemberRejected 非成员既拿不到 ticket，也无法建立连接。
func TestWSNonMemberRejected(t *testing.T) {
	env := setupWSTestEnv(t)

	status, ticket := issueWSTicket(t, env, env.outsider.Token, env.roomID)
	if status != http.StatusNotFound || ticket != "" {
		t.Fatalf("非成员换 ticket 应 404 且无 ticket，实际 status=%d ticket=%q", status, ticket)
	}

	// 即使持有伪造 ticket，建连也会被 4403 拒绝。
	wsURL := "ws" + strings.TrimPrefix(env.server.URL, "http") + "/api/v1/collab/rooms/" + env.roomID + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("建立 WS 连接失败: %v", err)
	}
	defer conn.CloseNow()
	if err := wsjson.Write(ctx, conn, map[string]any{
		"type": "hello", "protocolVersion": "1", "roomId": env.roomID, "ticket": "forged-ticket",
	}); err != nil {
		t.Fatalf("发送 hello 失败: %v", err)
	}
	_, _, err = conn.Read(ctx)
	if err == nil {
		// 服务端会先发 error 事件再关闭，继续读到关闭为止。
		_, closeErr := readUntilClosed(t, conn)
		err = closeErr
	}
	if err == nil {
		t.Fatal("伪造 ticket 建连应被拒绝")
	}
	if code := websocket.CloseStatus(err); code != 4401 {
		t.Fatalf("伪造 ticket 关闭码应为 4401，实际 %v", code)
	}
}

// TestWSSnapshotRedactsPrivateMetadata 快照绝不广播 private/local 与凭据字段。
func TestWSSnapshotRedactsPrivateMetadata(t *testing.T) {
	env := setupWSTestEnv(t)

	db, err := repository.DB()
	if err != nil {
		t.Fatalf("获取测试库失败: %v", err)
	}
	now := time.Now().UTC()
	// 直接落库一个带 private/凭据字段的节点，绕过 service 校验，验证广播层兜底脱敏。
	if err := db.Create(&model.RoomNode{
		RoomID: env.roomID, ID: "node_secret_001", Kind: "image", Title: "带密钥节点",
		PositionX: 1, PositionY: 2, Width: 10, Height: 10, Version: 1,
		Metadata:  `{"prompt":"公开提示词","imageTaskId":"task-secret","apiKey":"sk-secret","status":"running"}`,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("写入测试节点失败: %v", err)
	}

	_, ticket := issueWSTicket(t, env, env.editor.Token, env.roomID)
	conn := dialRoomWS(t, env, env.roomID, ticket)

	deadline := time.Now().Add(5 * time.Second)
	var found map[string]any
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var event map[string]any
		err := wsjson.Read(ctx, conn, &event)
		cancel()
		if err != nil {
			break
		}
		if event["type"] == "snapshot-end" {
			break
		}
		if event["type"] == "snapshot-node" {
			node, _ := event["node"].(map[string]any)
			if node != nil && node["id"] == "node_secret_001" {
				found = node
				break
			}
		}
	}
	if found == nil {
		t.Fatal("快照中未找到测试节点")
	}
	metadata, _ := found["metadata"].(map[string]any)
	if metadata == nil {
		t.Fatal("节点 metadata 不应为空（public 字段应保留）")
	}
	if metadata["prompt"] != "公开提示词" {
		t.Fatalf("public 字段 prompt 应保留，实际 %v", metadata["prompt"])
	}
	for _, forbidden := range []string{"imageTaskId", "apiKey", "status"} {
		if _, ok := metadata[forbidden]; ok {
			t.Fatalf("快照泄漏了禁止字段 %q：%v", forbidden, metadata)
		}
	}
}

// TestWSDisconnectOnMemberRemoval 成员被移除后，其已建立的 WS 连接被主动关闭。
func TestWSDisconnectOnMemberRemoval(t *testing.T) {
	env := setupWSTestEnv(t)

	_, viewerTicket := issueWSTicket(t, env, env.viewer.Token, env.roomID)
	viewerConn := dialRoomWS(t, env, env.roomID, viewerTicket)
	if end := waitForType(t, viewerConn, "snapshot-end"); end == nil {
		t.Fatal("viewer 未收到 snapshot-end")
	}

	// owner 通过 REST 移除 viewer。
	req, _ := http.NewRequest(http.MethodDelete,
		env.server.URL+"/api/v1/collab/rooms/"+env.roomID+"/members/"+env.viewer.User.ID, nil)
	req.Header.Set("Authorization", "Bearer "+env.owner.Token)
	resp, err := env.server.Client().Do(req)
	if err != nil {
		t.Fatalf("移除成员请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("移除成员应 200，实际 %d", resp.StatusCode)
	}

	code, readErr := readUntilClosed(t, viewerConn)
	if readErr == nil {
		t.Fatal("被移除成员的 WS 连接应被关闭")
	}
	if code != 4401 {
		t.Fatalf("被移除成员关闭码应为 4401，实际 %v", code)
	}
}
