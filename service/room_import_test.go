package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 旧画布安全导入测试（C04-02 / #25）。全部用例使用构造的脱敏旧格式 JSON，
// 不接触真实学校设备/凭据/旧数据库原件；媒体拉取走本地 httptest 假上游。

// importTestEnv 准备内存库、临时媒体目录与一个 owner 上下文（导入只允许 owner）。
func importTestEnv(t *testing.T) (context.Context, string) {
	t.Helper()
	setupCollabDB(t, "sqlite", ":memory:")
	config.Cfg.MediaDir = t.TempDir()
	config.Cfg.CollabMediaImportOrigins = ""
	ctx := collabContext(newID("import-user"))
	room, err := CreateRoom(ctx, "导入测试房间")
	if err != nil {
		t.Fatalf("创建导入测试房间失败: %v", err)
	}
	return ctx, room.ID
}

// oldImportNode 构造一个旧格式节点。
func oldImportNode(id, kind, title string, metadata map[string]any) model.RoomImportNode {
	return model.RoomImportNode{
		ID: id, Type: kind, Title: title,
		Position: model.RoomImportPos{X: 10, Y: 20}, Width: 320, Height: 240, Metadata: metadata,
	}
}

// initialImport 以 initial 模式导入给定快照。
func initialImport(t *testing.T, ctx context.Context, roomID string, snapshot model.RoomImportSnapshot) model.RoomImportReport {
	t.Helper()
	report, err := ImportRoomSnapshot(ctx, roomID, model.RoomImportRequest{Mode: "initial", Snapshot: snapshot})
	if err != nil {
		t.Fatalf("导入应成功，实际 %v", err)
	}
	return report
}

// importNodeMeta 解析房间图内某节点的 metadata JSON。
func importNodeMeta(t *testing.T, graph model.RoomGraph, nodeID string) map[string]any {
	t.Helper()
	node, ok := findNode(graph, nodeID)
	if !ok {
		t.Fatalf("房间图内找不到节点 %s", nodeID)
	}
	metadata := map[string]any{}
	if strings.TrimSpace(node.Metadata) == "" {
		return metadata
	}
	if err := json.Unmarshal([]byte(node.Metadata), &metadata); err != nil {
		t.Fatalf("解析节点 %s metadata 失败: %v", nodeID, err)
	}
	return metadata
}

func importMissingReasons(report model.RoomImportReport) map[string]string {
	reasons := map[string]string{}
	for _, item := range report.MissingMedia {
		reasons[item.NodeID] = item.Reason
	}
	return reasons
}

// TestImportOldSnapshotIntoEmptyRoom 正向：空房间导入成功，节点/连线正确，private/local 字段不导入。
func TestImportOldSnapshotIntoEmptyRoom(t *testing.T) {
	ctx, roomID := importTestEnv(t)
	snapshot := model.RoomImportSnapshot{
		ID: "qwen-h3-campus-workflow",
		Nodes: []model.RoomImportNode{
			oldImportNode("txt_prompt0001", "text", "提示词", map[string]any{
				"content": "黄昏下的海边小镇", "fontSize": float64(14),
				"status": "success", "progress": float64(100), "clientTaskId": "task-private-1",
			}),
			oldImportNode("img_shot000001", "image", "首帧", map[string]any{
				"prompt": "海边", "storageKey": "image:local-blob-1", "mimeType": "image/png",
			}),
			oldImportNode("vid_clip000001", "video", "成片", map[string]any{
				"prompt": "镜头推进", "content": "http://127.0.0.1:8189/clip.mp4",
			}),
			oldImportNode("grp_scene00001", "group", "第一幕", map[string]any{"groupId": "grp_scene00001"}),
			oldImportNode("cfg_config0001", "config", "配置", map[string]any{"content": `{"size":"16:9"}`}),
		},
		Connections: []model.RoomImportEdge{
			{ID: "edg_conn000001", FromNodeID: "txt_prompt0001", ToNodeID: "img_shot000001"},
			{ID: "edg_conn000002", FromNodeID: "img_shot000001", ToNodeID: "vid_clip000001"},
		},
	}
	report := initialImport(t, ctx, roomID, snapshot)

	if !report.Applied || report.Idempotent || report.Revision != 1 {
		t.Fatalf("导入报告状态异常: %+v", report)
	}
	if report.Nodes.Total != 5 || report.Nodes.Imported != 5 || report.Nodes.Skipped != 0 {
		t.Fatalf("节点统计异常: %+v", report.Nodes)
	}
	if report.Nodes.PrivateFieldsDropped != 1 || report.Nodes.LocalFieldsDropped != 2 {
		t.Fatalf("字段裁剪统计异常: private=%d local=%d", report.Nodes.PrivateFieldsDropped, report.Nodes.LocalFieldsDropped)
	}
	if report.Edges.Total != 2 || report.Edges.Imported != 2 {
		t.Fatalf("连线统计异常: %+v", report.Edges)
	}
	if report.Nodes.ByKind["text"] != 1 || report.Nodes.ByKind["image"] != 1 || report.Nodes.ByKind["video"] != 1 ||
		report.Nodes.ByKind["group"] != 1 || report.Nodes.ByKind["config"] != 1 {
		t.Fatalf("8 类节点字段摘要异常: %+v", report.Nodes.ByKind)
	}

	graph := graphOK(t, ctx, roomID)
	if len(graph.Nodes) != 5 || len(graph.Edges) != 2 {
		t.Fatalf("房间图数量异常: nodes=%d edges=%d", len(graph.Nodes), len(graph.Edges))
	}
	textMeta := importNodeMeta(t, graph, "txt_prompt0001")
	if textMeta["content"] != "黄昏下的海边小镇" {
		t.Fatalf("text 公共字段丢失: %+v", textMeta)
	}
	for _, privateKey := range []string{"clientTaskId", "status", "progress"} {
		if _, ok := textMeta[privateKey]; ok {
			t.Fatalf("private/local 字段 %q 不应导入公共图: %+v", privateKey, textMeta)
		}
	}

	// 媒体：image 的本地 storageKey 与 video 的 127.0.0.1 地址都不可达，标记缺失且不留悬空引用。
	if report.Media.Referenced != 2 || report.Media.Migrated != 0 || report.Media.Missing != 2 {
		t.Fatalf("媒体统计异常: %+v", report.Media)
	}
	reasons := importMissingReasons(report)
	if reasons["img_shot000001"] != "local_reference" {
		t.Fatalf("image 本地引用原因应为 local_reference，实际 %q", reasons["img_shot000001"])
	}
	if reasons["vid_clip000001"] != "source_not_allowed" {
		t.Fatalf("127.0.0.1 非白名单原因应为 source_not_allowed，实际 %q", reasons["vid_clip000001"])
	}
	for _, nodeID := range []string{"img_shot000001", "vid_clip000001"} {
		meta := importNodeMeta(t, graph, nodeID)
		if _, ok := meta["content"]; ok {
			t.Fatalf("节点 %s 不应残留 content 悬空引用: %+v", nodeID, meta)
		}
		if _, ok := meta["storageKey"]; ok {
			t.Fatalf("节点 %s 不应残留 storageKey 悬空引用: %+v", nodeID, meta)
		}
		if meta["mediaError"] == "" {
			t.Fatalf("节点 %s 应标记素材未同步: %+v", nodeID, meta)
		}
	}
}

// TestImportRejectedWhenRoomHasEdits 反向：房间已有成员操作（revision>0）时拒绝整份覆盖。
func TestImportRejectedWhenRoomHasEdits(t *testing.T) {
	ctx, roomID := importTestEnv(t)
	applyOK(t, ctx, roomID, envelope(roomID, "op_seedimport01",
		contract.Operation{Type: contract.OpCreate, Node: ptrNode(imageNode("img_seedimport01", "已有节点"))}))

	snapshot := model.RoomImportSnapshot{
		ID:    "legacy-project",
		Nodes: []model.RoomImportNode{oldImportNode("txt_prompt0001", "text", "提示词", map[string]any{"content": "x"})},
	}
	_, err := ImportRoomSnapshot(ctx, roomID, model.RoomImportRequest{Mode: "initial", Snapshot: snapshot})
	contractErr := assertContractCode(t, err, contract.CodeRoomNotEmpty)
	if contractErr.Status() != http.StatusConflict {
		t.Fatalf("room_not_empty 应为 409，实际 %d", contractErr.Status())
	}

	// 已有编辑不被旧快照覆盖：种子节点仍在，导入节点未写入。
	graph := graphOK(t, ctx, roomID)
	if _, ok := findNode(graph, "img_seedimport01"); !ok {
		t.Fatalf("已有成员节点被覆盖")
	}
	if _, ok := findNode(graph, "txt_prompt0001"); ok {
		t.Fatalf("房间非空时不应写入导入节点")
	}
}

// TestImportRejectedWhenRoomHasOtherMembers 反向：除 owner 外已有成员时拒绝。
func TestImportRejectedWhenRoomHasOtherMembers(t *testing.T) {
	ctx, roomID := importTestEnv(t)
	addRoomMember(t, roomID, "other-member-import", model.RoomRoleEditor)

	snapshot := model.RoomImportSnapshot{ID: "legacy", Nodes: []model.RoomImportNode{oldImportNode("txt_prompt0001", "text", "t", map[string]any{"content": "x"})}}
	_, err := ImportRoomSnapshot(ctx, roomID, model.RoomImportRequest{Mode: "initial", Snapshot: snapshot})
	assertContractCode(t, err, contract.CodeRoomNotEmpty)
}

// TestImportRejectedForNonOwner 反向：editor 不能初始化房间。
func TestImportRejectedForNonOwner(t *testing.T) {
	_, roomID := importTestEnv(t)
	editorCtx := collabContext("import-editor-user")
	addRoomMember(t, roomID, "import-editor-user", model.RoomRoleEditor)

	snapshot := model.RoomImportSnapshot{ID: "legacy", Nodes: []model.RoomImportNode{oldImportNode("txt_prompt0001", "text", "t", map[string]any{"content": "x"})}}
	_, err := ImportRoomSnapshot(editorCtx, roomID, model.RoomImportRequest{Mode: "initial", Snapshot: snapshot})
	if !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("editor 导入应返回 ErrRoomForbidden，实际 %v", err)
	}
}

// TestImportSkipsIllegalNodesAndReports 非法节点/连线被跳过并报告，合法节点仍导入。
func TestImportSkipsIllegalNodesAndReports(t *testing.T) {
	ctx, roomID := importTestEnv(t)
	snapshot := model.RoomImportSnapshot{
		ID: "legacy-mixed",
		Nodes: []model.RoomImportNode{
			oldImportNode("img_legal000001", "image", "合法", map[string]any{"prompt": "p"}),
			oldImportNode("bad_kind000001", "hologram", "未知类型", map[string]any{"prompt": "p"}),
			oldImportNode("abc", "image", "ID 过短", map[string]any{"prompt": "p"}),
			oldImportNode("txt_nocontent01", "text", "缺 content", map[string]any{"fontSize": float64(12)}),
			oldImportNode("img_secret00001", "image", "含凭据", map[string]any{"prompt": "p", "apiKey": "should-never-import"}),
			oldImportNode("img_zerowidth01", "image", "宽高非法", map[string]any{"prompt": "p"}),
		},
		Connections: []model.RoomImportEdge{
			{ID: "edg_ok00000001", FromNodeID: "img_legal000001", ToNodeID: "img_legal000001"},
			{ID: "edg_dangling001", FromNodeID: "img_legal000001", ToNodeID: "txt_nocontent01"},
		},
	}
	// 把 zerowidth 节点的宽度置 0（构造非法）。
	snapshot.Nodes[5].Width = 0

	report := initialImport(t, ctx, roomID, snapshot)

	if report.Nodes.Imported != 1 || report.Nodes.Total != 6 || report.Nodes.Skipped != 5 {
		t.Fatalf("非法节点统计异常: %+v", report.Nodes)
	}
	reasons := map[string]string{}
	for _, skipped := range report.SkippedNodes {
		reasons[skipped.ID] = skipped.Reason
	}
	wantReasons := map[string]string{
		"bad_kind000001":  "unknown_kind",
		"abc":             "invalid_id",
		"txt_nocontent01": contract.CodeInvalidPayload,
		"img_secret00001": contract.CodeForbiddenField,
		"img_zerowidth01": "invalid_size",
	}
	for id, want := range wantReasons {
		if reasons[id] != want {
			t.Fatalf("节点 %s 跳过原因应为 %s，实际 %q", id, want, reasons[id])
		}
	}
	// 报告不得回显凭据值。
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "should-never-import") {
		t.Fatalf("报告泄漏了凭据字段值: %s", encoded)
	}
	// 自环连线非法、悬空连线跳过；合法节点仍写入。
	if report.Edges.Imported != 0 || report.Edges.Skipped != 2 {
		t.Fatalf("连线统计异常: %+v", report.Edges)
	}
	graph := graphOK(t, ctx, roomID)
	if len(graph.Nodes) != 1 || len(graph.Edges) != 0 {
		t.Fatalf("合法节点应写入且无连线: nodes=%d edges=%d", len(graph.Nodes), len(graph.Edges))
	}
}

// TestImportIdempotentRepeat 幂等：重复导入同一份快照不产生重复节点、不推进 revision。
func TestImportIdempotentRepeat(t *testing.T) {
	ctx, roomID := importTestEnv(t)
	snapshot := model.RoomImportSnapshot{
		ID: "legacy-repeat",
		Nodes: []model.RoomImportNode{
			oldImportNode("txt_prompt0001", "text", "t", map[string]any{"content": "x"}),
			oldImportNode("img_shot000001", "image", "i", map[string]any{"prompt": "p"}),
		},
		Connections: []model.RoomImportEdge{{ID: "edg_conn000001", FromNodeID: "txt_prompt0001", ToNodeID: "img_shot000001"}},
	}
	first := initialImport(t, ctx, roomID, snapshot)
	second := initialImport(t, ctx, roomID, snapshot)

	if second.Idempotent != true || !second.Applied {
		t.Fatalf("第二次导入应为幂等重放: %+v", second)
	}
	if second.Revision != first.Revision {
		t.Fatalf("幂等重放不应推进 revision: first=%d second=%d", first.Revision, second.Revision)
	}
	graph := graphOK(t, ctx, roomID)
	if len(graph.Nodes) != 2 || len(graph.Edges) != 1 {
		t.Fatalf("幂等重放产生重复节点/连线: nodes=%d edges=%d", len(graph.Nodes), len(graph.Edges))
	}
	if graph.Revision != first.Revision {
		t.Fatalf("房间 revision 不应变化: %d != %d", graph.Revision, first.Revision)
	}
}

// TestImportPreviewIsReadOnlyAndSanitized 只读预扫描：给数量与摘要，不写库、不泄漏本地路径/密钥。
func TestImportPreviewIsReadOnlyAndSanitized(t *testing.T) {
	ctx, roomID := importTestEnv(t)
	snapshot := model.RoomImportSnapshot{
		ID: "legacy-preview",
		Nodes: []model.RoomImportNode{
			oldImportNode("img_shot000001", "image", "i", map[string]any{
				"prompt":       "p",
				"content":      "http://127.0.0.1:8190/private/secret-shot.png",
				"errorDetails": `C:\Users\secret-account\canvas\dump.png`,
				"clientTaskId": "task-secret",
			}),
		},
		Connections: []model.RoomImportEdge{{ID: "edg_conn000001", FromNodeID: "img_shot000001", ToNodeID: "img_shot000001"}},
	}
	report, err := ImportRoomSnapshot(ctx, roomID, model.RoomImportRequest{Mode: "preview", Snapshot: snapshot})
	if err != nil {
		t.Fatalf("预扫描失败: %v", err)
	}
	if report.Applied || report.Revision != 0 || report.Mode != "preview" {
		t.Fatalf("预扫描不应写库: %+v", report)
	}
	if report.Nodes.Total != 1 || report.Nodes.Imported != 1 {
		t.Fatalf("预扫描节点统计异常: %+v", report.Nodes)
	}
	if report.Media.Referenced != 1 || report.Media.Missing != 1 {
		t.Fatalf("预扫描媒体统计异常: %+v", report.Media)
	}
	if report.MissingMedia[0].Origin != "http://127.0.0.1:8190" {
		t.Fatalf("缺失媒体应只暴露 origin，实际 %q", report.MissingMedia[0].Origin)
	}
	encoded, _ := json.Marshal(report)
	for _, leaked := range []string{"secret-account", "secret-shot.png", "task-secret", `C:\Users`} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("预扫描报告泄漏了 %q: %s", leaked, encoded)
		}
	}
	// 只读：房间图仍为空、revision 仍为 0。
	graph := graphOK(t, ctx, roomID)
	if len(graph.Nodes) != 0 || graph.Revision != 0 {
		t.Fatalf("预扫描不应写入节点: nodes=%d revision=%d", len(graph.Nodes), graph.Revision)
	}
}

// TestImportRejectsCycle 候选图含环时整体失败（不部分导入）。
func TestImportRejectsCycle(t *testing.T) {
	ctx, roomID := importTestEnv(t)
	snapshot := model.RoomImportSnapshot{
		ID: "legacy-cycle",
		Nodes: []model.RoomImportNode{
			oldImportNode("img_cycle000001", "image", "a", map[string]any{"prompt": "p"}),
			oldImportNode("img_cycle000002", "image", "b", map[string]any{"prompt": "p"}),
		},
		Connections: []model.RoomImportEdge{
			{ID: "edg_cycle000001", FromNodeID: "img_cycle000001", ToNodeID: "img_cycle000002"},
			{ID: "edg_cycle000002", FromNodeID: "img_cycle000002", ToNodeID: "img_cycle000001"},
		},
	}
	_, err := ImportRoomSnapshot(ctx, roomID, model.RoomImportRequest{Mode: "initial", Snapshot: snapshot})
	assertContractCode(t, err, contract.CodeCycleDetected)
	graph := graphOK(t, ctx, roomID)
	if len(graph.Nodes) != 0 {
		t.Fatalf("含环导入应整体失败，实际写入了 %d 个节点", len(graph.Nodes))
	}
}

// TestImportMigratesAllowlistedMedia 正向：白名单来源媒体被拉取并登记为房间共享媒体。
func TestImportMigratesAllowlistedMedia(t *testing.T) {
	ctx, roomID := importTestEnv(t)
	png := testPNGBytes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer server.Close()
	config.Cfg.CollabMediaImportOrigins = server.URL

	snapshot := model.RoomImportSnapshot{
		ID: "legacy-media",
		Nodes: []model.RoomImportNode{
			oldImportNode("img_media000001", "image", "图", map[string]any{
				"prompt": "p", "content": server.URL + "/shot.png", "mimeType": "image/png",
			}),
		},
	}
	report := initialImport(t, ctx, roomID, snapshot)
	if report.Media.Referenced != 1 || report.Media.Migrated != 1 || report.Media.Missing != 0 || report.Media.Rejected != 0 {
		t.Fatalf("媒体迁移统计异常: %+v", report.Media)
	}
	graph := graphOK(t, ctx, roomID)
	meta := importNodeMeta(t, graph, "img_media000001")
	storageKey, _ := meta["storageKey"].(string)
	if !strings.HasPrefix(storageKey, "room:"+roomID+":") {
		t.Fatalf("迁移后 storageKey 应为房间媒体引用，实际 %q", storageKey)
	}
	if _, ok := meta["content"]; ok {
		t.Fatalf("迁移后不应残留原始 content: %+v", meta)
	}
	if meta["mimeType"] != "image/png" {
		t.Fatalf("迁移后应记录白名单内 mimeType: %+v", meta)
	}
	mediaList, err := RoomMediaList(ctx, roomID)
	if err != nil || mediaList.Total != 1 {
		t.Fatalf("房间共享媒体应登记 1 条，实际 total=%d err=%v", mediaList.Total, err)
	}
}

// TestImportRejectsActiveContentMedia 反向：白名单来源的 SVG 主动内容被拒，节点标记缺失。
func TestImportRejectsActiveContentMedia(t *testing.T) {
	ctx, roomID := importTestEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write(svgWithScriptBytes())
	}))
	defer server.Close()
	config.Cfg.CollabMediaImportOrigins = server.URL

	snapshot := model.RoomImportSnapshot{
		ID: "legacy-active",
		Nodes: []model.RoomImportNode{
			oldImportNode("img_active000001", "image", "svg", map[string]any{
				"prompt": "p", "content": server.URL + "/evil.svg", "mimeType": "image/svg+xml",
			}),
		},
	}
	report := initialImport(t, ctx, roomID, snapshot)
	if report.Media.Referenced != 1 || report.Media.Migrated != 0 || report.Media.Rejected != 1 || report.Media.Missing != 1 {
		t.Fatalf("主动内容媒体应被拒并计缺失: %+v", report.Media)
	}
	if importMissingReasons(report)["img_active000001"] != "unsupported_type" {
		t.Fatalf("主动内容缺失原因应为 unsupported_type: %+v", report.MissingMedia)
	}
	graph := graphOK(t, ctx, roomID)
	meta := importNodeMeta(t, graph, "img_active000001")
	if _, ok := meta["content"]; ok {
		t.Fatalf("被拒媒体不应残留 content: %+v", meta)
	}
	if meta["mediaError"] == "" {
		t.Fatalf("被拒媒体应标记素材未同步: %+v", meta)
	}
	mediaList, err := RoomMediaList(ctx, roomID)
	if err != nil || mediaList.Total != 0 {
		t.Fatalf("主动内容不得登记为房间媒体，实际 total=%d err=%v", mediaList.Total, err)
	}
}

// TestImportSeventySevenNodeSample 77 节点样本（含 54 个 127.0.0.1 媒体）导入/再读取比对，
// 报告可重复运行且不产生悬空引用。
func TestImportSeventySevenNodeSample(t *testing.T) {
	ctx, roomID := importTestEnv(t)
	const nodeCount = 77
	const loopbackMediaCount = 54

	nodes := make([]model.RoomImportNode, 0, nodeCount)
	connections := make([]model.RoomImportEdge, 0, nodeCount-1)
	for index := 0; index < nodeCount; index++ {
		id := "img_sample" + padImportIndex(index)
		metadata := map[string]any{"prompt": "prompt"}
		if index < loopbackMediaCount {
			metadata["content"] = "http://127.0.0.1:8190/shot" + padImportIndex(index) + ".png"
			metadata["mimeType"] = "image/png"
		}
		nodes = append(nodes, oldImportNode(id, "image", "节点", metadata))
		if index > 0 {
			connections = append(connections, model.RoomImportEdge{
				ID:         "edg_sample" + padImportIndex(index),
				FromNodeID: "img_sample" + padImportIndex(index-1),
				ToNodeID:   id,
			})
		}
	}
	snapshot := model.RoomImportSnapshot{ID: "qwen-h3-campus-workflow", Nodes: nodes, Connections: connections}

	report := initialImport(t, ctx, roomID, snapshot)
	if report.Nodes.Total != nodeCount || report.Nodes.Imported != nodeCount || report.Nodes.Skipped != 0 {
		t.Fatalf("77 节点样本统计异常: %+v", report.Nodes)
	}
	if report.Edges.Imported != nodeCount-1 {
		t.Fatalf("77 节点样本连线统计异常: %+v", report.Edges)
	}
	if report.Media.Referenced != loopbackMediaCount || report.Media.Migrated != 0 || report.Media.Missing != loopbackMediaCount {
		t.Fatalf("127.0.0.1 媒体应全部标记缺失: %+v", report.Media)
	}
	graph := graphOK(t, ctx, roomID)
	if len(graph.Nodes) != nodeCount || len(graph.Edges) != nodeCount-1 {
		t.Fatalf("再读取数量不一致: nodes=%d edges=%d", len(graph.Nodes), len(graph.Edges))
	}
	// 不产生悬空引用：54 个媒体节点均已清除 content/storageKey 并标记未同步。
	for index := 0; index < loopbackMediaCount; index++ {
		meta := importNodeMeta(t, graph, "img_sample"+padImportIndex(index))
		if _, ok := meta["content"]; ok {
			t.Fatalf("节点 %d 残留 127.0.0.1 悬空引用", index)
		}
		if meta["mediaError"] == "" {
			t.Fatalf("节点 %d 应标记素材未同步", index)
		}
	}
	// 可重复运行：再次导入幂等，数量不变。
	repeat := initialImport(t, ctx, roomID, snapshot)
	if !repeat.Idempotent {
		t.Fatalf("重复导入应为幂等: %+v", repeat)
	}
	graphAgain := graphOK(t, ctx, roomID)
	if len(graphAgain.Nodes) != nodeCount {
		t.Fatalf("重复导入后节点数变化: %d", len(graphAgain.Nodes))
	}
}

// TestImportReceiptPersisted 幂等回执落库，供断线重发命中（(roomId,userId,operationId)）。
func TestImportReceiptPersisted(t *testing.T) {
	ctx, roomID := importTestEnv(t)
	snapshot := model.RoomImportSnapshot{ID: "legacy-receipt", Nodes: []model.RoomImportNode{oldImportNode("txt_prompt0001", "text", "t", map[string]any{"content": "x"})}}
	initialImport(t, ctx, roomID, snapshot)

	user, _ := UserFromContext(ctx)
	digest, reportJSON, ok, err := repository.FindRoomImportReceipt(roomID, user.ID, importOperationID(roomID, snapshot.ID, mustDigest(t, snapshot)))
	if err != nil || !ok {
		t.Fatalf("导入回执应落库: ok=%v err=%v", ok, err)
	}
	if digest == "" || !strings.Contains(reportJSON, `"applied":true`) {
		t.Fatalf("回执内容异常: digest=%q report=%s", digest, reportJSON)
	}
}

func mustDigest(t *testing.T, snapshot model.RoomImportSnapshot) string {
	t.Helper()
	digest, err := importSnapshotDigest(snapshot)
	if err != nil {
		t.Fatalf("计算快照摘要失败: %v", err)
	}
	return digest
}

func padImportIndex(index int) string {
	return fmt.Sprintf("%03d", index)
}
