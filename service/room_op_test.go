package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// --- 测试辅助 ---

func collabTestRoom(t *testing.T, ctx context.Context) model.Room {
	t.Helper()
	room, err := CreateRoom(ctx, "协作图契约房间")
	if err != nil {
		t.Fatalf("创建测试房间失败: %v", err)
	}
	return room
}

func addRoomMember(t *testing.T, roomID, userID string, role model.RoomMemberRole) {
	t.Helper()
	now := time.Now().UTC()
	if err := repository.SaveRoomMember(model.RoomMember{
		RoomID: roomID, UserID: userID, Role: role, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入成员关系失败: %v", err)
	}
}

func envelope(roomID, opID string, ops ...contract.Operation) contract.Envelope {
	return contract.Envelope{
		ProtocolVersion: contract.ProtocolVersion,
		RoomID:          roomID,
		OperationID:     opID,
		Operations:      ops,
	}
}

func applyOK(t *testing.T, ctx context.Context, roomID string, env contract.Envelope) model.RoomBatchResult {
	t.Helper()
	result, err := ApplyRoomOperations(ctx, roomID, env)
	if err != nil {
		t.Fatalf("批次 %s 应成功，实际 %v", env.OperationID, err)
	}
	return result
}

func graphOK(t *testing.T, ctx context.Context, roomID string) model.RoomGraph {
	t.Helper()
	graph, err := ReadRoomGraph(ctx, roomID)
	if err != nil {
		t.Fatalf("读取房间图失败: %v", err)
	}
	return graph
}

func findNode(graph model.RoomGraph, id string) (model.RoomNode, bool) {
	for _, node := range graph.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return model.RoomNode{}, false
}

func assertContractCode(t *testing.T, err error, want string) *contract.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("期望契约错误 %s，实际无错误", want)
	}
	var contractErr *contract.Error
	if !errors.As(err, &contractErr) {
		t.Fatalf("期望 *contract.Error，实际 %T: %v", err, err)
	}
	if contractErr.Code != want {
		t.Fatalf("期望错误码 %s，实际 %s（%s）", want, contractErr.Code, contractErr.Message)
	}
	if contractErr.Status() != contract.StatusFor(want) {
		t.Fatalf("错误 %s 的 HTTP 状态应为 %d，实际 %d", want, contract.StatusFor(want), contractErr.Status())
	}
	return contractErr
}

func ptrNode(node contract.Node) *contract.Node { return &node }

func imageNode(id, title string) contract.Node {
	return contract.Node{
		ID: id, Kind: contract.NodeKindImage, Title: title, Version: 0,
		Position: contract.Position{X: 0, Y: 0}, Width: 10, Height: 10,
		Metadata: map[string]any{"prompt": "p"},
	}
}

// sampleCreateNodes 是 8 类节点的合法样例（version=0，用于 create）。
func sampleCreateNodes() []contract.Node {
	nodes := []contract.Node{
		{
			ID: "img_5Kd9xQ2mNp7", Kind: contract.NodeKindImage, Title: "首帧参考",
			Position: contract.Position{X: 120, Y: -40}, Width: 1024, Height: 1024,
			Metadata: map[string]any{
				"prompt": "赛博朋克城市夜景，霓虹倒影", "negativePrompt": "模糊, 低质量",
				"model": "seedream-4.0", "channelId": "channel_zh_01",
				"size": "16:9", "quality": "high", "count": 1,
				"generationMode": "image", "generationType": "generation",
				"storageKey": "rooms/r1/nodes/img_5Kd9xQ2mNp7.png", "mimeType": "image/png", "bytes": 204800,
				"naturalWidth": 1024, "naturalHeight": 1024, "freeResize": false,
			},
		},
		{
			ID: "pano_7Qw3eR9tYb1", Kind: contract.NodeKindPanorama, Title: "360 全景",
			Position: contract.Position{X: 1500, Y: 0}, Width: 2048, Height: 1024,
			Metadata: map[string]any{
				"panoramaSourcePrompt": "雪山日出全景", "panoramaFinalPrompt": "雪山日出全景，8k",
				"panoramaProjection": "equirectangular", "storageKey": "rooms/r1/nodes/pano.png",
			},
		},
		{
			ID: "vid_2Mn8bV4cXz6", Kind: contract.NodeKindVideo, Title: "15s 成片",
			Position: contract.Position{X: 2600, Y: 0}, Width: 1280, Height: 720,
			Metadata: map[string]any{
				"prompt": "镜头推进", "model": "minimax_h3_image_audio_to_video_v2_15s",
				"seconds": "15", "vquality": "768p", "generateAudio": "true", "watermark": "false",
				"firstFrameNodeId": "img_5Kd9xQ2mNp7", "storageKey": "rooms/r1/nodes/vid.mp4", "durationMs": 15000,
			},
		},
		{
			ID: "aud_9Pl4kH6jRt2", Kind: contract.NodeKindAudio, Title: "旁白",
			Position: contract.Position{X: 2600, Y: 800}, Width: 480, Height: 120,
			Metadata: map[string]any{
				"audioVoice": "zh_female_01", "audioFormat": "mp3", "audioSpeed": "1.0",
				"audioInstructions": "沉稳", "storageKey": "rooms/r1/nodes/aud.mp3", "durationMs": 8200,
			},
		},
		{
			ID: "txt_3Rt7yU1iOp5", Kind: contract.NodeKindText, Title: "提示词草稿",
			Position: contract.Position{X: -400, Y: 0}, Width: 360, Height: 200,
			Metadata: map[string]any{"content": "黄昏下的海边小镇，暖色调", "fontSize": 14},
		},
		{
			ID: "grp_6Yu2iO8pAs3", Kind: contract.NodeKindGroup, Title: "第一幕",
			Position: contract.Position{X: -600, Y: -200}, Width: 900, Height: 700,
			Metadata: map[string]any{"groupId": "grp_6Yu2iO8pAs3"},
		},
		{
			ID: "cfg_8Io3pA5sDf9", Kind: contract.NodeKindConfig, Title: "生成配置",
			Position: contract.Position{X: -400, Y: 400}, Width: 320, Height: 240,
			Metadata: map[string]any{"content": `{"size":"16:9","quality":"high","count":1}`},
		},
		{
			ID: "dir_1As4dF6gHj7", Kind: contract.NodeKindDirector, Title: "导演台",
			Position: contract.Position{X: 800, Y: -600}, Width: 960, Height: 540,
			Metadata: map[string]any{
				"directorProject":   map[string]any{"shots": []any{}},
				"primaryImageId":    "img_5Kd9xQ2mNp7",
				"references":        []any{"img_5Kd9xQ2mNp7"},
				"cameraControl":     map[string]any{"enabled": true, "camera": "dolly", "lens": "35mm", "focalLength": 35, "aperture": 2.8},
				"multiShot":         "true",
				"klingImageNodeIds": []any{"img_5Kd9xQ2mNp7"},
			},
		},
	}
	for index := range nodes {
		nodes[index].Version = 0
	}
	return nodes
}

// --- 用例 1：原子性、回滚与幂等 ---

func TestCollabRoomOperationsAtomicAndIdempotent(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	user := newID("collab-owner")
	ctx := collabContext(user)
	room := collabTestRoom(t, ctx)

	// 正例：一批创建两个节点，revision 从 0 -> 1。
	env := envelope(room.ID, "op_atomic_create_1",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_atomic_0001", "n1"))},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_atomic_0002", "n2"))},
	)
	result := applyOK(t, ctx, room.ID, env)
	if result.Revision != 1 || len(result.Changes) != 2 {
		t.Fatalf("首批应 revision=1 且 2 项变更，实际 %+v", result)
	}
	if result.Idempotent {
		t.Fatal("首次提交不应标记 idempotent")
	}
	graph := graphOK(t, ctx, room.ID)
	if len(graph.Nodes) != 2 || graph.Revision != 1 {
		t.Fatalf("图应有 2 节点 revision=1，实际 %+v", graph)
	}

	// 幂等：同一 operationId + 相同请求体 → 返回原回执，不重复创建、不推进 revision。
	replay := applyOK(t, ctx, room.ID, env)
	if !replay.Idempotent || replay.Revision != 1 || len(replay.Changes) != 2 {
		t.Fatalf("幂等重放应返回原回执，实际 %+v", replay)
	}
	graph = graphOK(t, ctx, room.ID)
	if len(graph.Nodes) != 2 || graph.Revision != 1 {
		t.Fatalf("幂等重放不得新增节点或推进 revision，实际 %+v", graph)
	}

	// 反例：同一 operationId + 不同请求体 → operation_id_reused（409）。
	_, err := ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_atomic_create_1",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_atomic_0003", "n3"))}))
	assertContractCode(t, err, contract.CodeOperationIDReused)

	// 反例：批次中后一条版本冲突 → 整批回滚，前一条 create 不落库，revision 不变。
	stale := 99
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_atomic_rollback_1",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_atomic_0004", "n4"))},
		contract.Operation{Type: contract.OpUpdate, ID: "img_atomic_0001", Version: &stale, Fields: map[string]any{"title": "x"}},
	))
	assertContractCode(t, err, contract.CodeVersionConflict)
	graph = graphOK(t, ctx, room.ID)
	if graph.Revision != 1 || len(graph.Nodes) != 2 {
		t.Fatalf("回滚后 revision 与节点数必须不变，实际 %+v", graph)
	}
	if _, ok := findNode(graph, "img_atomic_0004"); ok {
		t.Fatal("回滚批次中的 create 不应落库")
	}

	// 反例：凭据字段 → forbidden_field，整批不落库。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_atomic_forbidden_1",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(contract.Node{
			ID: "img_atomic_0005", Kind: contract.NodeKindImage, Title: "bad",
			Position: contract.Position{X: 1, Y: 1}, Width: 10, Height: 10,
			Metadata: map[string]any{"apiKey": "sk-live"},
		})}))
	assertContractCode(t, err, contract.CodeForbiddenField)
	graph = graphOK(t, ctx, room.ID)
	if graph.Revision != 1 || len(graph.Nodes) != 2 {
		t.Fatalf("非法批次不得改动房间，实际 %+v", graph)
	}
}

// --- 用例 2：版本冲突不覆盖（顺序 + 并发）---

func TestCollabRoomVersionConflictNoOverwrite(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	userA := newID("collab-user-a")
	userB := newID("collab-user-b")
	ctxA := collabContext(userA)
	ctxB := collabContext(userB)
	room := collabTestRoom(t, ctxA)
	addRoomMember(t, room.ID, userB, model.RoomRoleEditor)

	// A 创建节点（version=1）。
	applyOK(t, ctxA, room.ID, envelope(room.ID, "op_conflict_create",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_conflict_001", "原始"))}))

	// 两个账号在相同初始版本 1 上并发写入不同标题。
	expected := 1
	titles := []string{"A 的修改", "B 的修改"}
	ctxs := []context.Context{ctxA, ctxB}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := ApplyRoomOperations(ctxs[i], room.ID, envelope(room.ID, fmt.Sprintf("op_conflict_write_%d", i),
				contract.Operation{Type: contract.OpUpdate, ID: "img_conflict_001", Version: &expected, Fields: map[string]any{"title": titles[i]}}))
			errs[i] = err
		}(index)
	}
	wg.Wait()

	successes, conflicts := 0, 0
	for _, err := range errs {
		if err == nil {
			successes++
			continue
		}
		var contractErr *contract.Error
		if errors.As(err, &contractErr) && contractErr.Code == contract.CodeVersionConflict {
			conflicts++
			continue
		}
		t.Fatalf("并发写入出现意外错误: %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("并发两次相同版本写入应恰好 1 成功 1 冲突，实际 success=%d conflict=%d", successes, conflicts)
	}
	graph := graphOK(t, ctxA, room.ID)
	if graph.Revision != 2 {
		t.Fatalf("并发后 revision 应为 2，实际 %d", graph.Revision)
	}
	node, ok := findNode(graph, "img_conflict_001")
	if !ok || node.Version != 2 {
		t.Fatalf("节点版本应推进到 2，实际 %+v", node)
	}
	winnerTitle := node.Title
	if winnerTitle != "A 的修改" && winnerTitle != "B 的修改" {
		t.Fatalf("节点标题应保留成功者的修改，实际 %q", winnerTitle)
	}

	// 顺序反例：用过期版本 1 再写 → 409，内容不被覆盖。
	stale := 1
	_, err := ApplyRoomOperations(ctxB, room.ID, envelope(room.ID, "op_conflict_stale",
		contract.Operation{Type: contract.OpUpdate, ID: "img_conflict_001", Version: &stale, Fields: map[string]any{"title": "过时覆盖"}}))
	assertContractCode(t, err, contract.CodeVersionConflict)
	graph = graphOK(t, ctxA, room.ID)
	node, _ = findNode(graph, "img_conflict_001")
	if node.Title != winnerTitle {
		t.Fatalf("过期写入不得覆盖，期望 %q 实际 %q", winnerTitle, node.Title)
	}
}

// --- 用例 3：8 类节点合法样例与非法输入 ---

func TestCollabRoomNodeKindsAndValidation(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	// 正例：8 类节点合法样例在一个批次内全部通过。
	nodes := sampleCreateNodes()
	ops := make([]contract.Operation, 0, len(nodes))
	for _, node := range nodes {
		ops = append(ops, contract.Operation{Type: contract.OpCreate, Node: ptrNode(node)})
	}
	result := applyOK(t, ctx, room.ID, envelope(room.ID, "op_kinds_create", ops...))
	if len(result.Changes) != 8 {
		t.Fatalf("8 类节点应产生 8 项变更，实际 %d", len(result.Changes))
	}
	graph := graphOK(t, ctx, room.ID)
	if len(graph.Nodes) != 8 {
		t.Fatalf("图应有 8 个节点，实际 %d", len(graph.Nodes))
	}

	// 反例：未知 kind → invalid_payload。
	unknown := imageNode("img_unknown_001", "x")
	unknown.Kind = "widget"
	_, err := ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_kinds_unknown",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(unknown)}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 反例：text 节点缺 content → invalid_payload。
	missingContent := contract.Node{
		ID: "txt_missing_001", Kind: contract.NodeKindText, Title: "x",
		Position: contract.Position{}, Width: 10, Height: 10, Metadata: map[string]any{},
	}
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_kinds_missing_content",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(missingContent)}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 反例：update 使用不支持的顶层字段 → invalid_payload。
	version := 1
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_update_unknown_field",
		contract.Operation{Type: contract.OpUpdate, ID: "img_5Kd9xQ2mNp7", Version: &version, Fields: map[string]any{"bogus": 1}}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 反例：update 写入私有 metadata 字段 → invalid_payload。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_update_private_field",
		contract.Operation{Type: contract.OpUpdate, ID: "img_5Kd9xQ2mNp7", Version: &version,
			Fields: map[string]any{"metadata": map[string]any{"composerContent": "draft"}}}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 反例：update 写入凭据字段 → forbidden_field。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_update_secret_field",
		contract.Operation{Type: contract.OpUpdate, ID: "img_5Kd9xQ2mNp7", Version: &version,
			Fields: map[string]any{"metadata": map[string]any{"secretToken": "x"}}}))
	assertContractCode(t, err, contract.CodeForbiddenField)

	// 正例：合法的 title 增量更新推进节点版本。
	updated := applyOK(t, ctx, room.ID, envelope(room.ID, "op_update_title",
		contract.Operation{Type: contract.OpUpdate, ID: "img_5Kd9xQ2mNp7", Version: &version, Fields: map[string]any{"title": "新标题"}}))
	if updated.Revision != 2 {
		t.Fatalf("合法更新后 revision 应为 2，实际 %d", updated.Revision)
	}
	graph = graphOK(t, ctx, room.ID)
	node, _ := findNode(graph, "img_5Kd9xQ2mNp7")
	if node.Title != "新标题" || node.Version != 2 {
		t.Fatalf("更新结果错误: %+v", node)
	}
}

// --- 用例 4：连线归属、引用完整性与角色 ---

func TestCollabRoomEdgeIntegrityAndRoles(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	owner := newID("collab-owner")
	editor := newID("collab-editor")
	viewer := newID("collab-viewer")
	ctxOwner := collabContext(owner)
	ctxEditor := collabContext(editor)
	ctxViewer := collabContext(viewer)
	room := collabTestRoom(t, ctxOwner)
	addRoomMember(t, room.ID, editor, model.RoomRoleEditor)
	addRoomMember(t, room.ID, viewer, model.RoomRoleViewer)

	applyOK(t, ctxOwner, room.ID, envelope(room.ID, "op_edge_nodes",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(contract.Node{
			ID: "txt_edge_0001", Kind: contract.NodeKindText, Title: "源",
			Position: contract.Position{}, Width: 10, Height: 10, Metadata: map[string]any{"content": "a"}})},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_edge_0001", "目标"))},
	))

	// 正例：editor 连接两个同房间节点。
	applyOK(t, ctxEditor, room.ID, envelope(room.ID, "op_edge_connect",
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_edge_0001", FromNodeID: "txt_edge_0001", ToNodeID: "img_edge_0001"}}))
	graph := graphOK(t, ctxOwner, room.ID)
	if len(graph.Edges) != 1 || graph.Edges[0].FromNodeID != "txt_edge_0001" || graph.Edges[0].ToNodeID != "img_edge_0001" {
		t.Fatalf("连线未正确落库: %+v", graph.Edges)
	}

	// 反例：连线引用不存在的节点 → invalid_payload，整批回滚。
	_, err := ApplyRoomOperations(ctxEditor, room.ID, envelope(room.ID, "op_edge_missing_endpoint",
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_edge_0002", FromNodeID: "txt_edge_0001", ToNodeID: "img_missing_9999"}}))
	assertContractCode(t, err, contract.CodeInvalidPayload)
	graph = graphOK(t, ctxOwner, room.ID)
	if len(graph.Edges) != 1 {
		t.Fatalf("端点不存在的连线不得落库，实际 %d 条", len(graph.Edges))
	}

	// 反例：同一批次内连线端点自环 → invalid_payload（契约 ValidateOperation 拦截）。
	_, err = ApplyRoomOperations(ctxEditor, room.ID, envelope(room.ID, "op_edge_selfloop",
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_edge_0003", FromNodeID: "img_edge_0001", ToNodeID: "img_edge_0001"}}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 反例：同批次重复连线 ID → duplicate_target。
	_, err = ApplyRoomOperations(ctxEditor, room.ID, envelope(room.ID, "op_edge_duplicate",
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_edge_0004", FromNodeID: "txt_edge_0001", ToNodeID: "img_edge_0001"}},
		contract.Operation{Type: contract.OpDisconnect, ID: "edg_edge_0004", Version: intPtr(1)}))
	assertContractCode(t, err, contract.CodeDuplicateTarget)

	// 反例：连线成环 → cycle_detected，整批回滚。
	_, err = ApplyRoomOperations(ctxEditor, room.ID, envelope(room.ID, "op_edge_cycle",
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_edge_0005", FromNodeID: "img_edge_0001", ToNodeID: "txt_edge_0001"}}))
	assertContractCode(t, err, contract.CodeCycleDetected)
	graph = graphOK(t, ctxOwner, room.ID)
	if len(graph.Edges) != 1 {
		t.Fatalf("成环批次不得落库，实际 %d 条连线", len(graph.Edges))
	}

	// 反例：disconnect 版本不符 → edge_changed。
	wrongVersion := 99
	_, err = ApplyRoomOperations(ctxEditor, room.ID, envelope(room.ID, "op_edge_disconnect_stale",
		contract.Operation{Type: contract.OpDisconnect, ID: "edg_edge_0001", Version: &wrongVersion}))
	assertContractCode(t, err, contract.CodeEdgeChanged)

	// 正例：disconnect 正确版本成功。
	applyOK(t, ctxEditor, room.ID, envelope(room.ID, "op_edge_disconnect_ok",
		contract.Operation{Type: contract.OpDisconnect, ID: "edg_edge_0001", Version: intPtr(1)}))
	graph = graphOK(t, ctxOwner, room.ID)
	if len(graph.Edges) != 0 {
		t.Fatalf("连线应已删除，实际 %d 条", len(graph.Edges))
	}

	// 反例：viewer 只读 → forbidden。
	_, err = ApplyRoomOperations(ctxViewer, room.ID, envelope(room.ID, "op_viewer_write",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_viewer_0001", "v"))}))
	assertContractCode(t, err, contract.CodeForbidden)

	// 反例：非成员 → not_found（不泄漏房间是否存在）。
	_, err = ApplyRoomOperations(collabContext(newID("collab-stranger")), room.ID, envelope(room.ID, "op_stranger_write",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_stranger_0001", "s"))}))
	assertContractCode(t, err, contract.CodeNotFound)

	// 反例：未登录 → unauthorized。
	_, err = ApplyRoomOperations(context.Background(), room.ID, envelope(room.ID, "op_anonymous_write",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_anon_0001", "a"))}))
	assertContractCode(t, err, contract.CodeUnauthorized)
}

// --- 用例 5：删除后更新返回 node_deleted ---

func TestCollabRoomNodeDeletedTombstone(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	applyOK(t, ctx, room.ID, envelope(room.ID, "op_delete_create",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_delete_0001", "n"))}))
	applyOK(t, ctx, room.ID, envelope(room.ID, "op_delete_node",
		contract.Operation{Type: contract.OpDelete, ID: "img_delete_0001", Version: intPtr(1)}))

	graph := graphOK(t, ctx, room.ID)
	if _, ok := findNode(graph, "img_delete_0001"); ok {
		t.Fatal("已删除节点不应出现在公共图中")
	}

	// 更新已删除节点 → node_deleted（409）。
	_, err := ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_delete_update_again",
		contract.Operation{Type: contract.OpUpdate, ID: "img_delete_0001", Version: intPtr(2), Fields: map[string]any{"title": "x"}}))
	assertContractCode(t, err, contract.CodeNodeDeleted)

	// 重复删除 → node_deleted（409）。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_delete_again",
		contract.Operation{Type: contract.OpDelete, ID: "img_delete_0001", Version: intPtr(2)}))
	assertContractCode(t, err, contract.CodeNodeDeleted)

	// 重复创建同一 ID → node_exists（409）。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_delete_recreate",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_delete_0001", "n"))}))
	assertContractCode(t, err, contract.CodeNodeExists)
}

// --- 用例 6：协议版本不匹配 ---

func TestCollabRoomProtocolVersionMismatch(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	bad := envelope(room.ID, "op_protocol_mismatch",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_proto_0001", "p"))})
	bad.ProtocolVersion = "2"
	_, err := ApplyRoomOperations(ctx, room.ID, bad)
	assertContractCode(t, err, contract.CodeProtocolMismatch)

	// 路径房间 ID 与请求体不一致 → invalid_payload。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(newID("room-other"), "op_room_mismatch",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_room_0001", "r"))}))
	assertContractCode(t, err, contract.CodeInvalidPayload)
}

func intPtr(value int) *int { return &value }
