package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
)

// 本文件覆盖 #9：8 类节点在协作公共图的完整语义、连线语义与多图引用顺序语义。
// 复用 room_op_test.go 中的 setupCollabDB / envelope / applyOK / graphOK 等辅助。

func kindNode(id string, kind contract.NodeKind, metadata map[string]any) contract.Node {
	return contract.Node{
		ID: id, Kind: kind, Title: id, Version: 0,
		Position: contract.Position{X: 0, Y: 0}, Width: 10, Height: 10,
		Metadata: metadata,
	}
}

func canonicalJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return string(raw)
}

func requireNode(t *testing.T, graph model.RoomGraph, id string) model.RoomNode {
	t.Helper()
	node, ok := findNode(graph, id)
	if !ok {
		t.Fatalf("公共图中缺少节点 %s", id)
	}
	return node
}

// --- 用例 1：8 类节点公共字段与可公开 metadata 往返语义一致 ---

func TestCollabGraphEightKindsRoundTrip(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	samples := sampleCreateNodes()
	ops := make([]contract.Operation, 0, len(samples))
	for _, node := range samples {
		ops = append(ops, contract.Operation{Type: contract.OpCreate, Node: ptrNode(node)})
	}
	result := applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_kinds_create", ops...))
	if len(result.Changes) != len(samples) {
		t.Fatalf("8 类节点应产生 %d 项变更，实际 %d", len(samples), len(result.Changes))
	}

	graph := graphOK(t, ctx, room.ID)
	if len(graph.Nodes) != len(samples) {
		t.Fatalf("图应有 %d 个节点，实际 %d", len(samples), len(graph.Nodes))
	}
	seenKinds := map[contract.NodeKind]bool{}
	for _, sample := range samples {
		node := requireNode(t, graph, sample.ID)
		seenKinds[contract.NodeKind(node.Kind)] = true
		if node.Kind != string(sample.Kind) || node.Title != sample.Title {
			t.Fatalf("节点 %s 基础字段不一致: %+v", sample.ID, node)
		}
		if node.PositionX != sample.Position.X || node.PositionY != sample.Position.Y ||
			node.Width != sample.Width || node.Height != sample.Height || node.Version != 1 {
			t.Fatalf("节点 %s 几何/版本不一致: %+v", sample.ID, node)
		}
		// 可公开 metadata 必须逐字段往返一致（数字经 JSON 归一化后字符串相等）。
		if got, want := canonicalJSON(t, nodeMetadata(t, node)), canonicalJSON(t, sample.Metadata); got != want {
			t.Fatalf("节点 %s metadata 往返不一致\n got=%s\nwant=%s", sample.ID, got, want)
		}
	}
	for _, kind := range contract.NodeKinds() {
		if !seenKinds[kind] {
			t.Fatalf("8 类节点覆盖缺失: %s", kind)
		}
	}
}

// --- 用例 2：private / local 字段归属与非法字段拒绝 ---

func TestCollabGraphPrivateAndLocalMetadataIsolation(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	// 反例：create 携带 private 字段 → invalid_payload，整批不落库。
	_, err := ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_private_create",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("img_sem_priv_1", contract.NodeKindImage,
			map[string]any{"prompt": "p", "composerContent": "未确认草稿"}))}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 反例：create 携带凭据类字段 → forbidden_field。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_secret_create",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("img_sem_sec_1", contract.NodeKindImage,
			map[string]any{"prompt": "p", "apiKey": "sk-live"}))}))
	assertContractCode(t, err, contract.CodeForbiddenField)

	// 正例：create 携带 local 字段 → 接受，但 local 字段不落库、不出现在公共图。
	applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_local_create",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("img_sem_loc_1", contract.NodeKindImage,
			map[string]any{"prompt": "p", "status": "loading", "progress": 42, "startedAt": 123}))}))
	node := requireNode(t, graphOK(t, ctx, room.ID), "img_sem_loc_1")
	metadata := nodeMetadata(t, node)
	if metadata["prompt"] != "p" {
		t.Fatalf("public 字段 prompt 应保留: %+v", metadata)
	}
	for _, local := range []string{"status", "progress", "startedAt"} {
		if _, ok := metadata[local]; ok {
			t.Fatalf("local 字段 %s 不得进入公共图: %+v", local, metadata)
		}
	}

	// 反例：update 写入 private metadata → invalid_payload。
	version := 1
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_private_update",
		contract.Operation{Type: contract.OpUpdate, ID: "img_sem_loc_1", Version: &version,
			Fields: map[string]any{"metadata": map[string]any{"imageTaskId": "task-1"}}}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 反例：update 写入凭据字段 → forbidden_field。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_secret_update",
		contract.Operation{Type: contract.OpUpdate, ID: "img_sem_loc_1", Version: &version,
			Fields: map[string]any{"metadata": map[string]any{"sessionToken": "x"}}}))
	assertContractCode(t, err, contract.CodeForbiddenField)

	// 正例：update 写入 local 字段 → 接受但不落库。
	applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_local_update",
		contract.Operation{Type: contract.OpUpdate, ID: "img_sem_loc_1", Version: &version,
			Fields: map[string]any{"metadata": map[string]any{"progress": 99, "quality": "high"}}}))
	node = requireNode(t, graphOK(t, ctx, room.ID), "img_sem_loc_1")
	metadata = nodeMetadata(t, node)
	if metadata["quality"] != "high" {
		t.Fatalf("update 的 public 字段应落库: %+v", metadata)
	}
	if _, ok := metadata["progress"]; ok {
		t.Fatalf("update 的 local 字段不得落库: %+v", metadata)
	}
}

// --- 用例 3：有序多图引用（学校 H3 1..9 张、顺序稳定、重复/越界拒绝）---

func TestCollabGraphOrderedReferences(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	// 正例：1 张、2 张、9 张有序引用均合法，且顺序稳定可读回。
	for _, count := range []int{1, 2, 9} {
		refs := make([]any, count)
		for index := range refs {
			refs[index] = "img_sem_ref_" + string(rune('a'+index))
		}
		nodeID := "vid_sem_ref_" + string(rune('0'+count))
		applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_ref_"+string(rune('0'+count)),
			contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode(nodeID, contract.NodeKindVideo,
				map[string]any{"prompt": "p", "references": refs}))}))
		node := requireNode(t, graphOK(t, ctx, room.ID), nodeID)
		got, ok := nodeMetadata(t, node)["references"].([]any)
		if !ok || len(got) != count {
			t.Fatalf("%d 张引用应读回 %d 张，实际 %#v", count, count, nodeMetadata(t, node)["references"])
		}
		for index := range refs {
			if got[index] != refs[index] {
				t.Fatalf("%d 张引用顺序被破坏：第 %d 张 got=%v want=%v", count, index, got[index], refs[index])
			}
		}
	}

	// 正例：空 references 等价于「无引用」，不触发 H3 数量校验。
	applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_ref_empty",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("vid_sem_ref_empty", contract.NodeKindVideo,
			map[string]any{"prompt": "p", "references": []any{}}))}))

	// 反例：10 张 → invalid_payload（超过 H3 上限）。
	ten := make([]any, 10)
	for index := range ten {
		ten[index] = "img_sem_ref_" + string(rune('a'+index))
	}
	_, err := ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_ref_ten",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("vid_sem_ref_ten", contract.NodeKindVideo,
			map[string]any{"prompt": "p", "references": ten}))}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 反例：重复引用 → invalid_payload。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_ref_dup",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("vid_sem_ref_dup", contract.NodeKindVideo,
			map[string]any{"prompt": "p", "references": []any{"img_sem_ref_a", "img_sem_ref_a"}}))}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 反例：references 非数组 → invalid_payload。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_ref_type",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("vid_sem_ref_type", contract.NodeKindVideo,
			map[string]any{"prompt": "p", "references": "img_sem_ref_a"}))}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 反例：update 把视频引用改成重复 → invalid_payload。
	version := 1
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_ref_update_dup",
		contract.Operation{Type: contract.OpUpdate, ID: "vid_sem_ref_1", Version: &version,
			Fields: map[string]any{"metadata": map[string]any{"references": []any{"img_sem_ref_a", "img_sem_ref_a"}}}}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// H3 规则只作用于视频节点的多图引用；其他节点的 references 不受 1..9 限制。
	manyRefs := make([]any, 10)
	for index := range manyRefs {
		manyRefs[index] = "img_sem_ref_" + string(rune('a'+index))
	}
	applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_ref_director",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("dir_sem_ref_1", contract.NodeKindDirector,
			map[string]any{"directorProject": map[string]any{"shots": []any{}}, "references": manyRefs}))}))
}

// --- 用例 4：分组归属与删除分组时的成员处理 ---

func TestCollabGraphGroupMembershipAndDeletion(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	// 正例：分组节点 + 两个归属成员一次性创建。
	applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_group_create",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("grp_sem_0001", contract.NodeKindGroup,
			map[string]any{"groupId": "grp_sem_0001"}))},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("img_sem_grp_1", contract.NodeKindImage,
			map[string]any{"prompt": "p", "groupId": "grp_sem_0001"}))},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("img_sem_grp_2", contract.NodeKindImage,
			map[string]any{"prompt": "p", "groupId": "grp_sem_0001"}))},
	))
	graph := graphOK(t, ctx, room.ID)
	for _, child := range []string{"img_sem_grp_1", "img_sem_grp_2"} {
		if got := nodeMetadata(t, requireNode(t, graph, child))["groupId"]; got != "grp_sem_0001" {
			t.Fatalf("成员 %s 的 groupId 应为 grp_sem_0001，实际 %v", child, got)
		}
	}

	// 反例：groupId 指向已存在的非分组节点 → invalid_payload。
	_, err := ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_group_bad_target",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("img_sem_grp_bad", contract.NodeKindImage,
			map[string]any{"prompt": "p", "groupId": "img_sem_grp_1"}))}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 正例：删除分组节点 → 成员保留但解除归属（groupId 清空），版本推进。
	result := applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_group_delete",
		contract.Operation{Type: contract.OpDelete, ID: "grp_sem_0001", Version: intPtr(1)}))
	if len(result.Changes) != 3 {
		t.Fatalf("删除含 2 名成员的分组应产生 3 项变更（1 删除 + 2 解除归属），实际 %d", len(result.Changes))
	}
	graph = graphOK(t, ctx, room.ID)
	if _, ok := findNode(graph, "grp_sem_0001"); ok {
		t.Fatal("分组节点应已删除")
	}
	for _, child := range []string{"img_sem_grp_1", "img_sem_grp_2"} {
		node := requireNode(t, graph, child)
		if _, ok := nodeMetadata(t, node)["groupId"]; ok {
			t.Fatalf("成员 %s 的 groupId 应被清空: %+v", child, node.Metadata)
		}
		if node.Version != 2 {
			t.Fatalf("成员 %s 解除归属后版本应为 2，实际 %d", child, node.Version)
		}
	}
}

// --- 用例 5：删除节点时关联连线的一致处理（含撤销恢复）---

func TestCollabGraphNodeDeletionCascadesEdges(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_cascade_create",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("txt_sem_cas_1", contract.NodeKindText,
			map[string]any{"content": "a"}))},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("img_sem_cas_1", contract.NodeKindImage,
			map[string]any{"prompt": "p"}))},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("vid_sem_cas_1", contract.NodeKindVideo,
			map[string]any{"prompt": "p"}))},
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_sem_cas_1", FromNodeID: "txt_sem_cas_1", ToNodeID: "img_sem_cas_1"}},
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_sem_cas_2", FromNodeID: "img_sem_cas_1", ToNodeID: "vid_sem_cas_1"}},
	))

	// 删除中间节点 img：两条关联连线必须一并软删除，不得留下悬空边。
	result := applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_cascade_delete",
		contract.Operation{Type: contract.OpDelete, ID: "img_sem_cas_1", Version: intPtr(1)}))
	edgeDeletes := 0
	for _, change := range result.Changes {
		if change.Type == "edge-delete" {
			edgeDeletes++
		}
	}
	if edgeDeletes != 2 {
		t.Fatalf("删除节点应级联删除 2 条关联连线，实际 %d（changes=%+v）", edgeDeletes, result.Changes)
	}
	graph := graphOK(t, ctx, room.ID)
	if len(graph.Edges) != 0 {
		t.Fatalf("关联连线应全部删除，实际 %d 条", len(graph.Edges))
	}
	if len(graph.Nodes) != 2 {
		t.Fatalf("仅节点被删除，其余节点应保留，实际 %d", len(graph.Nodes))
	}

	// 撤销删除：节点与其关联连线应一起恢复（级联删除同样进入历史）。
	entry := historyByOperation(t, ctx, room.ID, "op_sem_cascade_delete")
	if _, err := UndoRoomHistory(ctx, room.ID, entry.ID); err != nil {
		t.Fatalf("撤销删除应成功，实际 %v", err)
	}
	graph = graphOK(t, ctx, room.ID)
	if len(graph.Nodes) != 3 || len(graph.Edges) != 2 {
		t.Fatalf("撤销删除应恢复 3 节点 2 连线，实际 nodes=%d edges=%d", len(graph.Nodes), len(graph.Edges))
	}

	// 正例：删除无连线节点只产生 1 项变更，不影响既有连线。
	solo := applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_solo_delete",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("img_sem_solo_1", contract.NodeKindImage,
			map[string]any{"prompt": "p"}))}))
	if len(solo.Changes) != 1 {
		t.Fatalf("创建无连线节点应 1 项变更，实际 %d", len(solo.Changes))
	}
	deleted := applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_solo_delete_2",
		contract.Operation{Type: contract.OpDelete, ID: "img_sem_solo_1", Version: intPtr(1)}))
	if len(deleted.Changes) != 1 {
		t.Fatalf("删除无连线节点应只产生 1 项变更，实际 %d", len(deleted.Changes))
	}
	if len(graphOK(t, ctx, room.ID).Edges) != 2 {
		t.Fatal("删除无连线节点不得影响既有连线")
	}
}

// --- 用例 6：连线语义（正常跨类型连接不被误拒；四类负例）---

func TestCollabGraphEdgeSemantics(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_edge_nodes",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("txt_sem_edge_1", contract.NodeKindText, map[string]any{"content": "a"}))},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("img_sem_edge_1", contract.NodeKindImage, map[string]any{"prompt": "p"}))},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("vid_sem_edge_1", contract.NodeKindVideo, map[string]any{"prompt": "p"}))},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("aud_sem_edge_1", contract.NodeKindAudio, map[string]any{"prompt": "p"}))},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("pano_sem_edge_1", contract.NodeKindPanorama, map[string]any{"panoramaProjection": "equirectangular"}))},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("cfg_sem_edge_1", contract.NodeKindConfig, map[string]any{"content": `{"size":"16:9"}`}))},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("dir_sem_edge_1", contract.NodeKindDirector, map[string]any{"directorProject": map[string]any{"shots": []any{}}}))},
	))

	// 正例：跨类型普通连接（text→image→video→audio、image→panorama、config→text、director→image）
	// 均被接受，不存在参考项目那样的 targetPort 白名单误拒。
	applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_edge_normal",
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_sem_n_1", FromNodeID: "txt_sem_edge_1", ToNodeID: "img_sem_edge_1"}},
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_sem_n_2", FromNodeID: "img_sem_edge_1", ToNodeID: "vid_sem_edge_1"}},
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_sem_n_3", FromNodeID: "vid_sem_edge_1", ToNodeID: "aud_sem_edge_1"}},
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_sem_n_4", FromNodeID: "img_sem_edge_1", ToNodeID: "pano_sem_edge_1"}},
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_sem_n_5", FromNodeID: "cfg_sem_edge_1", ToNodeID: "txt_sem_edge_1"}},
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_sem_n_6", FromNodeID: "dir_sem_edge_1", ToNodeID: "img_sem_edge_1"}},
	))
	if len(graphOK(t, ctx, room.ID).Edges) != 6 {
		t.Fatalf("普通连接应全部落库，实际 %d 条", len(graphOK(t, ctx, room.ID).Edges))
	}

	// 负例 1：端点不存在 → invalid_payload。
	_, err := ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_edge_missing",
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_sem_bad_1", FromNodeID: "txt_sem_edge_1", ToNodeID: "img_sem_missing_9"}}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 负例 2：自环 → invalid_payload。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_edge_self",
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_sem_bad_2", FromNodeID: "img_sem_edge_1", ToNodeID: "img_sem_edge_1"}}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 负例 3：同批次重复连线 ID → duplicate_target。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_edge_dup_id",
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_sem_bad_3", FromNodeID: "txt_sem_edge_1", ToNodeID: "img_sem_edge_1"}},
		contract.Operation{Type: contract.OpDisconnect, ID: "edg_sem_bad_3", Version: intPtr(1)}))
	assertContractCode(t, err, contract.CodeDuplicateTarget)

	// 负例 4：不同 ID 但同端点方向的重复边 → duplicate_target。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_edge_dup_pair",
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_sem_bad_4", FromNodeID: "txt_sem_edge_1", ToNodeID: "img_sem_edge_1"}}))
	assertContractCode(t, err, contract.CodeDuplicateTarget)

	// 负例 5：成环 → cycle_detected。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_edge_cycle",
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_sem_bad_5", FromNodeID: "img_sem_edge_1", ToNodeID: "txt_sem_edge_1"}}))
	assertContractCode(t, err, contract.CodeCycleDetected)

	if len(graphOK(t, ctx, room.ID).Edges) != 6 {
		t.Fatal("所有负例批次都必须整批回滚，连线数保持 6")
	}
}

// --- 用例 7：媒体引用字段（storageKey/mediaId）在 panorama/video/audio 往返 ---

func TestCollabGraphMediaReferencesRoundTrip(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	mediaKey := "room:" + room.ID + ":5f8d3a2b1c9e"
	applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_media_create",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("pano_sem_med_1", contract.NodeKindPanorama,
			map[string]any{"panoramaProjection": "equirectangular", "storageKey": mediaKey, "mimeType": "image/png"}))},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("vid_sem_med_1", contract.NodeKindVideo,
			map[string]any{"prompt": "p", "storageKey": mediaKey, "mimeType": "video/mp4", "durationMs": 15000}))},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("aud_sem_med_1", contract.NodeKindAudio,
			map[string]any{"audioVoice": "zh_female_01", "storageKey": mediaKey, "mimeType": "audio/mpeg", "durationMs": 8200}))},
	))

	graph := graphOK(t, ctx, room.ID)
	for _, id := range []string{"pano_sem_med_1", "vid_sem_med_1", "aud_sem_med_1"} {
		metadata := nodeMetadata(t, requireNode(t, graph, id))
		if metadata["storageKey"] != mediaKey {
			t.Fatalf("节点 %s storageKey 往返失败: %v", id, metadata["storageKey"])
		}
		// storageKey 是公共媒体引用（room:<roomId>:<mediaId>），mediaId 必须可完整读回。
		if !strings.HasPrefix(metadata["storageKey"].(string), "room:"+room.ID+":") {
			t.Fatalf("节点 %s storageKey 前缀丢失: %v", id, metadata["storageKey"])
		}
	}
}

// --- 用例 8：config 内容必须为 JSON 对象、text content 必填、directorProject 上限 ---

func TestCollabGraphContentAndSizeLimits(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	// 反例：config content 非法 JSON → invalid_payload。
	_, err := ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_cfg_bad_json",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("cfg_sem_bad_1", contract.NodeKindConfig,
			map[string]any{"content": "not json"}))}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 反例：config content 是 JSON 数组而非对象 → invalid_payload。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_cfg_array",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("cfg_sem_bad_2", contract.NodeKindConfig,
			map[string]any{"content": `[1,2,3]`}))}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 正例：config content 为合法 JSON 对象。
	applyOK(t, ctx, room.ID, envelope(room.ID, "op_sem_cfg_ok",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("cfg_sem_ok_1", contract.NodeKindConfig,
			map[string]any{"content": `{"size":"16:9","quality":"high"}`}))}))

	// 反例：text 节点缺少 content → invalid_payload。
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_text_missing",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("txt_sem_bad_1", contract.NodeKindText,
			map[string]any{"fontSize": 14}))}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 反例：directorProject 大对象超过 metadata 上限（1 MiB）→ invalid_payload。
	huge := map[string]any{"directorProject": map[string]any{"blob": strings.Repeat("a", (1<<20)+4096)}}
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_dir_huge",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(kindNode("dir_sem_bad_1", contract.NodeKindDirector, huge))}))
	assertContractCode(t, err, contract.CodeInvalidPayload)

	// 反例：update 把 config content 改成非法 JSON → invalid_payload，原值不变。
	version := 1
	_, err = ApplyRoomOperations(ctx, room.ID, envelope(room.ID, "op_sem_cfg_update_bad",
		contract.Operation{Type: contract.OpUpdate, ID: "cfg_sem_ok_1", Version: &version,
			Fields: map[string]any{"metadata": map[string]any{"content": "broken"}}}))
	assertContractCode(t, err, contract.CodeInvalidPayload)
	if got := nodeMetadata(t, requireNode(t, graphOK(t, ctx, room.ID), "cfg_sem_ok_1"))["content"]; got != `{"size":"16:9","quality":"high"}` {
		t.Fatalf("非法更新不得覆盖 config content，实际 %v", got)
	}
}
