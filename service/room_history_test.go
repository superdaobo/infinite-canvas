package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
)

// --- 测试辅助 ---

func historyList(t *testing.T, ctx context.Context, roomID string) []model.RoomHistory {
	t.Helper()
	list, err := ListRoomHistory(ctx, roomID, 0)
	if err != nil {
		t.Fatalf("读取操作历史失败: %v", err)
	}
	return list.Items
}

func historyByOperation(t *testing.T, ctx context.Context, roomID, operationID string) model.RoomHistory {
	t.Helper()
	for _, item := range historyList(t, ctx, roomID) {
		if item.OperationID == operationID {
			return item
		}
	}
	t.Fatalf("未找到 operationId=%s 的历史记录", operationID)
	return model.RoomHistory{}
}

// --- 用例 1：审计完整 + 撤销/重做恢复节点与连线 + 幂等 ---

func TestCollabRoomHistoryAuditUndoRedoRestoresNodeAndEdges(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	// 一个批次创建 text 节点 + image 节点 + 连线。
	applyOK(t, ctx, room.ID, envelope(room.ID, "op_hist_create_graph",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(contract.Node{
			ID: "txt_hist_0001", Kind: contract.NodeKindText, Title: "源",
			Position: contract.Position{X: 1, Y: 2}, Width: 10, Height: 10,
			Metadata: map[string]any{"content": "a"}})},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_hist_0001", "目标"))},
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_hist_0001", FromNodeID: "txt_hist_0001", ToNodeID: "img_hist_0001"}},
	))

	// 审计：谁、何时、什么操作、前后版本。
	items := historyList(t, ctx, room.ID)
	if len(items) != 1 {
		t.Fatalf("应恰好 1 条历史，实际 %d", len(items))
	}
	entry := items[0]
	if entry.ActorID == "" || entry.OperationID != "op_hist_create_graph" ||
		entry.Status != model.RoomHistoryApplied || entry.Revision != 1 || entry.CreatedAt.IsZero() {
		t.Fatalf("历史审计字段不完整: %+v", entry)
	}
	if len(entry.Ops) != 3 {
		t.Fatalf("历史应含 3 个目标明细，实际 %d", len(entry.Ops))
	}
	for _, op := range entry.Ops {
		if op.Before != nil || op.After == nil || op.Version != 1 || !op.Active {
			t.Fatalf("create/connect 历史明细的前后版本错误: %+v", op)
		}
	}
	if entry.Summary == "" {
		t.Fatal("历史摘要不应为空")
	}

	// 撤销：节点与关联连线一起恢复（回到创建前，即都移除）。
	result, err := UndoRoomHistory(ctx, room.ID, entry.ID)
	if err != nil {
		t.Fatalf("撤销自己的操作应成功，实际 %v", err)
	}
	if result.Idempotent {
		t.Fatal("首次撤销不应标记 idempotent")
	}
	graph := graphOK(t, ctx, room.ID)
	if len(graph.Nodes) != 0 || len(graph.Edges) != 0 {
		t.Fatalf("撤销后图应为空，实际 nodes=%d edges=%d", len(graph.Nodes), len(graph.Edges))
	}
	if got := historyList(t, ctx, room.ID)[0].Status; got != model.RoomHistoryUndone {
		t.Fatalf("撤销后历史状态应为 undone，实际 %s", got)
	}

	// 重复撤销幂等：不重复执行、不推进 revision。
	revisionBefore := graphOK(t, ctx, room.ID).Revision
	again, err := UndoRoomHistory(ctx, room.ID, entry.ID)
	if err != nil {
		t.Fatalf("重复撤销应幂等成功，实际 %v", err)
	}
	if !again.Idempotent {
		t.Fatal("重复撤销应标记 idempotent")
	}
	if graphOK(t, ctx, room.ID).Revision != revisionBefore {
		t.Fatal("重复撤销不得推进 revision")
	}

	// 重做：节点与连线一起恢复。
	redone, err := RedoRoomHistory(ctx, room.ID, entry.ID)
	if err != nil {
		t.Fatalf("重做应成功，实际 %v", err)
	}
	if redone.Idempotent {
		t.Fatal("首次重做不应标记 idempotent")
	}
	graph = graphOK(t, ctx, room.ID)
	if len(graph.Nodes) != 2 || len(graph.Edges) != 1 {
		t.Fatalf("重做应恢复 2 节点 1 连线，实际 nodes=%d edges=%d", len(graph.Nodes), len(graph.Edges))
	}
	if _, ok := findNode(graph, "img_hist_0001"); !ok {
		t.Fatal("重做应恢复 image 节点")
	}

	// 重复重做幂等。
	againRedo, err := RedoRoomHistory(ctx, room.ID, entry.ID)
	if err != nil || !againRedo.Idempotent {
		t.Fatalf("重复重做应幂等，实际 err=%v result=%+v", err, againRedo)
	}

	// 状态可反复切换：再次撤销仍生效。
	if _, err := UndoRoomHistory(ctx, room.ID, entry.ID); err != nil {
		t.Fatalf("再次撤销应成功，实际 %v", err)
	}
	graph = graphOK(t, ctx, room.ID)
	if len(graph.Nodes) != 0 || len(graph.Edges) != 0 {
		t.Fatalf("再次撤销后图应为空，实际 nodes=%d edges=%d", len(graph.Nodes), len(graph.Edges))
	}
}

// --- 用例 2：撤销按用户隔离，撤销他人操作被拒 ---

func TestCollabRoomHistoryUndoIsolatedPerUser(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	owner := newID("collab-owner")
	editor := newID("collab-editor")
	ctxOwner := collabContext(owner)
	ctxEditor := collabContext(editor)
	room := collabTestRoom(t, ctxOwner)
	addRoomMember(t, room.ID, editor, model.RoomRoleEditor)

	applyOK(t, ctxEditor, room.ID, envelope(room.ID, "op_hist_editor_create",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_hist_iso_1", "e"))}))
	entry := historyByOperation(t, ctxEditor, room.ID, "op_hist_editor_create")
	if entry.ActorID != editor {
		t.Fatalf("历史归属应为 editor，实际 %s", entry.ActorID)
	}

	// 反例：owner 撤销 editor 的操作 → forbidden，节点保留。
	_, err := UndoRoomHistory(ctxOwner, room.ID, entry.ID)
	assertContractCode(t, err, contract.CodeForbidden)
	if _, ok := findNode(graphOK(t, ctxOwner, room.ID), "img_hist_iso_1"); !ok {
		t.Fatal("他人撤销被拒后节点必须保留")
	}
	if historyList(t, ctxEditor, room.ID)[0].Status != model.RoomHistoryApplied {
		t.Fatal("被拒的撤销不得改动历史状态")
	}

	// 正例：editor 撤销自己的操作成功。
	if _, err := UndoRoomHistory(ctxEditor, room.ID, entry.ID); err != nil {
		t.Fatalf("本人撤销应成功，实际 %v", err)
	}
	if _, ok := findNode(graphOK(t, ctxEditor, room.ID), "img_hist_iso_1"); ok {
		t.Fatal("本人撤销后节点应移除")
	}
}

// --- 用例 3：相关节点被他人修改后撤销返回冲突且不覆盖 ---

func TestCollabRoomHistoryUndoConflictWhenModifiedByOther(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	owner := newID("collab-owner")
	editor := newID("collab-editor")
	ctxOwner := collabContext(owner)
	ctxEditor := collabContext(editor)
	room := collabTestRoom(t, ctxOwner)
	addRoomMember(t, room.ID, editor, model.RoomRoleEditor)

	// editor 创建节点（version=1）。
	applyOK(t, ctxEditor, room.ID, envelope(room.ID, "op_hist_conf_create",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_hist_conf_1", "原始"))}))
	entry := historyByOperation(t, ctxEditor, room.ID, "op_hist_conf_create")

	// owner 修改同一节点（version=2）。
	applyOK(t, ctxOwner, room.ID, envelope(room.ID, "op_hist_conf_update",
		contract.Operation{Type: contract.OpUpdate, ID: "img_hist_conf_1", Version: intPtr(1),
			Fields: map[string]any{"title": "他人修改"}}))

	// editor 撤销自己的创建 → version_conflict，且不覆盖他人修改。
	_, err := UndoRoomHistory(ctxEditor, room.ID, entry.ID)
	conflict := assertContractCode(t, err, contract.CodeVersionConflict)
	if conflict.Details["reason"] != "history_invalidated" {
		t.Fatalf("冲突应说明历史失效，实际 details=%+v", conflict.Details)
	}
	node, ok := findNode(graphOK(t, ctxEditor, room.ID), "img_hist_conf_1")
	if !ok || node.Title != "他人修改" || node.Version != 2 {
		t.Fatalf("撤销冲突不得覆盖他人修改，实际 %+v", node)
	}
	if historyList(t, ctxEditor, room.ID)[0].Status != model.RoomHistoryApplied {
		t.Fatal("冲突后历史状态应保持 applied")
	}
}

// --- 用例 4：撤销“删除节点 + 断开连线”批次，节点与连线一起恢复 ---

func TestCollabRoomHistoryUndoDeleteRestoresNodeAndEdge(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	applyOK(t, ctx, room.ID, envelope(room.ID, "op_hist_del_create",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(contract.Node{
			ID: "txt_hist_del_1", Kind: contract.NodeKindText, Title: "源",
			Position: contract.Position{}, Width: 10, Height: 10, Metadata: map[string]any{"content": "a"}})},
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_hist_del_1", "目标"))},
		contract.Operation{Type: contract.OpConnect, Edge: &contract.Edge{ID: "edg_hist_del_1", FromNodeID: "txt_hist_del_1", ToNodeID: "img_hist_del_1"}},
	))
	applyOK(t, ctx, room.ID, envelope(room.ID, "op_hist_del_delete",
		contract.Operation{Type: contract.OpDisconnect, ID: "edg_hist_del_1", Version: intPtr(1)},
		contract.Operation{Type: contract.OpDelete, ID: "txt_hist_del_1", Version: intPtr(1)},
	))
	graph := graphOK(t, ctx, room.ID)
	if len(graph.Nodes) != 1 || len(graph.Edges) != 0 {
		t.Fatalf("删除后应剩 1 节点 0 连线，实际 nodes=%d edges=%d", len(graph.Nodes), len(graph.Edges))
	}

	entry := historyByOperation(t, ctx, room.ID, "op_hist_del_delete")
	if _, err := UndoRoomHistory(ctx, room.ID, entry.ID); err != nil {
		t.Fatalf("撤销删除应成功，实际 %v", err)
	}
	graph = graphOK(t, ctx, room.ID)
	if len(graph.Nodes) != 2 || len(graph.Edges) != 1 {
		t.Fatalf("撤销删除应恢复节点与连线，实际 nodes=%d edges=%d", len(graph.Nodes), len(graph.Edges))
	}
	if _, ok := findNode(graph, "txt_hist_del_1"); !ok {
		t.Fatal("撤销删除应恢复 text 节点")
	}
}

// --- 用例 5：连续拖动合并为一条历史，并可整体撤销 ---

func TestCollabRoomHistoryMergesConsecutiveDrags(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	ctx := collabContext(newID("collab-owner"))
	room := collabTestRoom(t, ctx)

	applyOK(t, ctx, room.ID, envelope(room.ID, "op_hist_merge_create",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_hist_merge_1", "n"))}))
	// 三次连续拖动，每次单独批次（版本 1→2→3→4）。
	for index, x := range []float64{10, 20, 30} {
		version := 1 + index
		applyOK(t, ctx, room.ID, envelope(room.ID, fmt.Sprintf("op_hist_merge_drag_%d", index),
			contract.Operation{Type: contract.OpUpdate, ID: "img_hist_merge_1", Version: intPtr(version),
				Fields: map[string]any{"position": map[string]any{"x": x, "y": 0}}}))
	}

	items := historyList(t, ctx, room.ID)
	if len(items) != 2 {
		t.Fatalf("连续拖动应合并为 1 条更新历史（共 2 条），实际 %d", len(items))
	}
	drag := items[0]
	if len(drag.Ops) != 1 || drag.Ops[0].Op != "update" || drag.Ops[0].ID != "img_hist_merge_1" {
		t.Fatalf("拖动历史明细错误: %+v", drag.Ops)
	}
	if drag.Ops[0].Merged != 2 {
		t.Fatalf("应合并 2 次后续批次，实际 %d", drag.Ops[0].Merged)
	}
	if drag.Ops[0].Version != 4 {
		t.Fatalf("合并历史应记录最新版本 4，实际 %d", drag.Ops[0].Version)
	}
	if drag.Ops[0].Before == nil || drag.Ops[0].Before.Node.PositionX != 0 {
		t.Fatalf("合并历史应保留最初 Before（x=0）: %+v", drag.Ops[0].Before)
	}
	if drag.Ops[0].After == nil || drag.Ops[0].After.Node.PositionX != 30 {
		t.Fatalf("合并历史应记录最新 After（x=30）: %+v", drag.Ops[0].After)
	}

	// 撤销合并历史 → 回到拖动前坐标 0。
	if _, err := UndoRoomHistory(ctx, room.ID, drag.ID); err != nil {
		t.Fatalf("撤销拖动历史应成功，实际 %v", err)
	}
	node, ok := findNode(graphOK(t, ctx, room.ID), "img_hist_merge_1")
	if !ok || node.PositionX != 0 {
		t.Fatalf("撤销拖动应恢复 x=0，实际 %+v", node)
	}

	// 重做后节点版本为 6；非 position-only 更新不合并，产生独立历史。
	if _, err := RedoRoomHistory(ctx, room.ID, drag.ID); err != nil {
		t.Fatalf("重做拖动历史应成功，实际 %v", err)
	}
	applyOK(t, ctx, room.ID, envelope(room.ID, "op_hist_merge_title",
		contract.Operation{Type: contract.OpUpdate, ID: "img_hist_merge_1", Version: intPtr(6),
			Fields: map[string]any{"title": "改名"}}))
	items = historyList(t, ctx, room.ID)
	if len(items) != 3 {
		t.Fatalf("非拖动更新不应合并，历史应为 3 条，实际 %d", len(items))
	}
	if items[0].Ops[0].Op != "update" || items[0].Ops[0].Merged != 0 {
		t.Fatalf("最新历史应为独立更新，实际 %+v", items[0].Ops[0])
	}
}

// --- 用例 6：未登录 / 只读成员 / 不存在历史的负例 ---

func TestCollabRoomHistoryPermissionAndNotFound(t *testing.T) {
	setupCollabDB(t, "sqlite", ":memory:")
	owner := newID("collab-owner")
	viewer := newID("collab-viewer")
	ctxOwner := collabContext(owner)
	ctxViewer := collabContext(viewer)
	room := collabTestRoom(t, ctxOwner)
	addRoomMember(t, room.ID, viewer, model.RoomRoleViewer)

	applyOK(t, ctxOwner, room.ID, envelope(room.ID, "op_hist_perm_create",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_hist_perm_1", "n"))}))
	entry := historyByOperation(t, ctxOwner, room.ID, "op_hist_perm_create")

	// 反例：未登录 → unauthorized。
	_, err := UndoRoomHistory(context.Background(), room.ID, entry.ID)
	assertContractCode(t, err, contract.CodeUnauthorized)

	// 反例：viewer 不能撤销（只读）→ forbidden。
	_, err = UndoRoomHistory(ctxViewer, room.ID, entry.ID)
	assertContractCode(t, err, contract.CodeForbidden)

	// 反例：历史不存在 → not_found。
	_, err = UndoRoomHistory(ctxOwner, room.ID, "hist_missing_0001")
	assertContractCode(t, err, contract.CodeNotFound)

	// 反例：非成员读取历史 → not_found（不泄漏房间是否存在）。
	_, err = ListRoomHistory(collabContext(newID("collab-stranger")), room.ID, 0)
	assertContractCode(t, err, contract.CodeNotFound)
}
