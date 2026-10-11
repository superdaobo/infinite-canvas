package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 学校成功结果安全上传与共享节点回填测试（C03-05 / #22）。
// 全部用例只走 mock 上游与本地假数据，不接触真实学校设备/凭据。

// useResultDownloader 替换结果媒体下载器为确定性假实现，返回恢复函数。
func useResultDownloader(data []byte, contentType string, err error) func() {
	original := SchoolResultDownloader
	SchoolResultDownloader = func(context.Context, string) ([]byte, string, error) {
		return data, contentType, err
	}
	return func() { SchoolResultDownloader = original }
}

// testPNGBytes 返回一张合法的 1x1 PNG，用于图片渠道结果校验。
func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	const encoded = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("解码测试 PNG 失败: %v", err)
	}
	return raw
}

// setupSchoolResultEnv 在共享测试库上准备房间、媒体目录与结果下载器。
func setupSchoolResultEnv(t *testing.T, data []byte, contentType string, downloadErr error) *schoolTestEnv {
	t.Helper()
	env := setupSchoolTestEnv(t)
	config.Cfg.MediaDir = t.TempDir()
	restore := useResultDownloader(data, contentType, downloadErr)
	t.Cleanup(restore)
	return env
}

func roomNodeByID(t *testing.T, roomID, nodeID string) (model.RoomNode, bool) {
	t.Helper()
	row, ok, err := repository.FindRoomNode(roomID, nodeID)
	if err != nil {
		t.Fatalf("读取节点失败: %v", err)
	}
	return row, ok
}

func nodeMetadata(t *testing.T, row model.RoomNode) map[string]any {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal([]byte(row.Metadata), &metadata); err != nil {
		t.Fatalf("解析节点 metadata 失败: %v", err)
	}
	return metadata
}

// TestUploadSchoolTaskResultSuccessBackfillsNode 成功用例：结果上传 → 任务 succeeded →
// 房间图新增图片节点（含 taskId/submittedBy）→ 媒体可读。
func TestUploadSchoolTaskResultSuccessBackfillsNode(t *testing.T) {
	env := setupSchoolResultEnv(t, testPNGBytes(t), "image/png", nil)
	task := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-result-ok",
		model.SchoolTaskParams{Prompt: "海边小镇"})
	if _, ok := mustClaim(t, "executor-result"); !ok {
		t.Fatal("应能领取任务")
	}

	backfill, err := UploadSchoolTaskResult(context.Background(), "executor-result", task.ID,
		SchoolResultInput{ResultURLs: []string{"https://example.invalid/result-0.png"}})
	if err != nil {
		t.Fatalf("结果回填应成功，实际 %v", err)
	}
	if backfill.Idempotent {
		t.Fatal("首次回填不应标记为幂等命中")
	}
	if backfill.Task.Status != model.SchoolTaskSucceeded {
		t.Fatalf("任务应记为 succeeded，实际 %s", backfill.Task.Status)
	}
	if backfill.Node.Kind != string(contract.NodeKindImage) {
		t.Fatalf("应生成图片节点，实际 kind=%s", backfill.Node.Kind)
	}
	metadata := nodeMetadata(t, backfill.Node)
	if metadata["taskId"] != task.ID {
		t.Fatalf("节点应保留任务关联 taskId，实际 %v", metadata["taskId"])
	}
	if metadata["submittedBy"] != env.userA {
		t.Fatalf("节点应保留提交人 submittedBy，实际 %v", metadata["submittedBy"])
	}
	if metadata["storageKey"] == "" || metadata["mimeType"] != "image/png" {
		t.Fatalf("节点应引用房间媒体，实际 %v", metadata)
	}

	// 房间图确有该节点，且 revision 已推进（提交成功才会推进）。
	graph, err := ReadRoomGraph(env.ctxA, env.roomID)
	if err != nil {
		t.Fatalf("读取房间图失败: %v", err)
	}
	if len(graph.Nodes) != 1 || graph.Nodes[0].ID != backfill.Node.ID {
		t.Fatalf("房间图应新增 1 个结果节点，实际 %+v", graph.Nodes)
	}
	if graph.Revision < 1 {
		t.Fatalf("节点写入应推进房间 revision，实际 %d", graph.Revision)
	}

	// 媒体可读且内容完整（回填的字节与源一致）。
	_, file, err := RoomMediaContent(env.ctxA, env.roomID, backfill.Media.ID)
	if err != nil {
		t.Fatalf("回填媒体应可读，实际 %v", err)
	}
	defer file.Close()
	readBack, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("读取媒体失败: %v", err)
	}
	if string(readBack) != string(testPNGBytes(t)) {
		t.Fatal("回填媒体内容与源不一致")
	}

	// 任务记录已落库为 succeeded。
	persisted, ok, err := repository.FindSchoolTaskByID(task.ID)
	if err != nil || !ok || persisted.Status != model.SchoolTaskSucceeded {
		t.Fatalf("任务应持久化为 succeeded，实际 ok=%v task=%+v err=%v", ok, persisted, err)
	}
}

// TestUploadSchoolTaskResultIdempotent 幂等用例：重复 finish/upload 只产生一份节点与一份媒体。
func TestUploadSchoolTaskResultIdempotent(t *testing.T) {
	env := setupSchoolResultEnv(t, testPNGBytes(t), "image/png", nil)
	task := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-result-idem",
		model.SchoolTaskParams{Prompt: "同一结果"})
	if _, ok := mustClaim(t, "executor-idem"); !ok {
		t.Fatal("应能领取任务")
	}

	input := SchoolResultInput{ResultURLs: []string{"https://example.invalid/result-0.png"}}
	first, err := UploadSchoolTaskResult(context.Background(), "executor-idem", task.ID, input)
	if err != nil {
		t.Fatalf("首次回填失败: %v", err)
	}
	second, err := UploadSchoolTaskResult(context.Background(), "executor-idem", task.ID, input)
	if err != nil {
		t.Fatalf("重复回填应按幂等成功返回，实际 %v", err)
	}
	if !second.Idempotent {
		t.Fatal("重复回填应标记幂等命中")
	}
	if second.Node.ID != first.Node.ID {
		t.Fatalf("重复回填应命中同一节点，实际 %s vs %s", second.Node.ID, first.Node.ID)
	}

	graph, err := ReadRoomGraph(env.ctxA, env.roomID)
	if err != nil {
		t.Fatalf("读取房间图失败: %v", err)
	}
	if len(graph.Nodes) != 1 {
		t.Fatalf("重复回填不得产生多份节点，实际 %d", len(graph.Nodes))
	}
	list, err := RoomMediaList(env.ctxA, env.roomID)
	if err != nil {
		t.Fatalf("读取媒体列表失败: %v", err)
	}
	if list.Total != 1 {
		t.Fatalf("重复回填不得产生多份媒体，实际 %d", list.Total)
	}
}

// TestUploadSchoolTaskResultIntegrityFailureKeepsSlot 反例：完整性/可访问性校验不通过
// → 不记为 succeeded，任务保持 running，单执行槽不提前解锁。
func TestUploadSchoolTaskResultIntegrityFailureKeepsSlot(t *testing.T) {
	env := setupSchoolResultEnv(t, nil, "", errors.New("结果地址不可访问"))
	task := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-result-bad",
		model.SchoolTaskParams{Prompt: "坏结果"})
	mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-result-next",
		model.SchoolTaskParams{Prompt: "下一个"})
	if _, ok := mustClaim(t, "executor-bad"); !ok {
		t.Fatal("应能领取任务")
	}

	// 反例 1：结果媒体不可访问。
	if _, err := UploadSchoolTaskResult(context.Background(), "executor-bad", task.ID,
		SchoolResultInput{ResultURLs: []string{"https://example.invalid/result-0.png"}}); contractCode(err) != CodeSchoolResultUnreachable {
		t.Fatalf("不可访问的结果应 result_unreachable，实际 %v", err)
	}
	persisted, _, err := repository.FindSchoolTaskByID(task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if persisted.Status != model.SchoolTaskRunning {
		t.Fatalf("校验失败时任务必须保持 running，实际 %s", persisted.Status)
	}

	// 反例 2：结果类型与渠道不匹配（文本冒充图片）。
	restore := useResultDownloader([]byte("not an image"), "text/plain", nil)
	defer restore()
	if _, err := UploadSchoolTaskResult(context.Background(), "executor-bad", task.ID,
		SchoolResultInput{ResultURLs: []string{"https://example.invalid/result-0.png"}}); contractCode(err) != CodeSchoolResultInvalid {
		t.Fatalf("类型不匹配的结果应 result_invalid，实际 %v", err)
	}

	// 反例 3：缺少结果来源。
	if _, err := UploadSchoolTaskResult(context.Background(), "executor-bad", task.ID,
		SchoolResultInput{}); contractCode(err) != CodeSchoolResultMissing {
		t.Fatalf("缺少结果来源应 result_missing，实际 %v", err)
	}

	// 校验全部失败后任务仍 running，且未创建任何节点/媒体。
	persisted, _, err = repository.FindSchoolTaskByID(task.ID)
	if err != nil || persisted.Status != model.SchoolTaskRunning {
		t.Fatalf("任务应保持 running，实际 %+v err=%v", persisted, err)
	}
	graph, err := ReadRoomGraph(env.ctxA, env.roomID)
	if err != nil {
		t.Fatalf("读取房间图失败: %v", err)
	}
	if len(graph.Nodes) != 0 {
		t.Fatalf("校验失败不得创建结果节点，实际 %d", len(graph.Nodes))
	}
	list, err := RoomMediaList(env.ctxA, env.roomID)
	if err != nil || list.Total != 0 {
		t.Fatalf("校验失败不得写入房间媒体，实际 total=%d err=%v", list.Total, err)
	}

	// 单执行槽保持：下一个任务仍被阻塞，绝不提前解锁。
	claim, err := ClaimSchoolTask(context.Background(), "executor-other")
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	if claim.Task != nil || !claim.Blocked || claim.RunningTaskID != task.ID {
		t.Fatalf("校验失败时不得解锁下一个任务，实际 %+v", claim)
	}
}

// TestUploadSchoolTaskResultRejectsWrongExecutor 反例：非持有该任务的设备不得回填。
func TestUploadSchoolTaskResultRejectsWrongExecutor(t *testing.T) {
	env := setupSchoolResultEnv(t, testPNGBytes(t), "image/png", nil)
	task := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-result-wrong-exec",
		model.SchoolTaskParams{Prompt: "越权"})
	if _, ok := mustClaim(t, "executor-owner"); !ok {
		t.Fatal("应能领取任务")
	}
	if _, err := UploadSchoolTaskResult(context.Background(), "executor-intruder", task.ID,
		SchoolResultInput{ResultURLs: []string{"https://example.invalid/result-0.png"}}); contractCode(err) != CodeSchoolTaskNotRunning {
		t.Fatalf("非持有设备应 not_running，实际 %v", err)
	}
	persisted, _, err := repository.FindSchoolTaskByID(task.ID)
	if err != nil || persisted.Status != model.SchoolTaskRunning {
		t.Fatalf("越权回填后任务应保持 running，实际 %+v err=%v", persisted, err)
	}
}

// TestSchoolTaskFailureSanitizedAndNoNode 失败用例：任务 failed 记录脱敏原因、不创建结果节点，
// 且失败信息不泄露底层凭据。
func TestSchoolTaskFailureSanitizedAndNoNode(t *testing.T) {
	env := setupSchoolResultEnv(t, testPNGBytes(t), "image/png", nil)
	task := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-failure",
		model.SchoolTaskParams{Prompt: "会失败"})
	if _, ok := mustClaim(t, "executor-fail"); !ok {
		t.Fatal("应能领取任务")
	}

	leaky := "cookie=session_abc123; Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.abcdefghij; password=hunter2"
	if _, err := FinishSchoolTaskForExecutor(context.Background(), "executor-fail", task.ID,
		model.SchoolTaskFailed, leaky); err != nil {
		t.Fatalf("上报失败状态失败: %v", err)
	}
	persisted, ok, err := repository.FindSchoolTaskByID(task.ID)
	if err != nil || !ok {
		t.Fatalf("读取任务失败: ok=%v err=%v", ok, err)
	}
	if persisted.Status != model.SchoolTaskFailed {
		t.Fatalf("任务应记为 failed，实际 %s", persisted.Status)
	}
	for _, secret := range []string{"session_abc123", "eyJhbGci", "hunter2", "Bearer eyJ"} {
		if strings.Contains(persisted.Error, secret) {
			t.Fatalf("失败信息泄露凭据 %q：%s", secret, persisted.Error)
		}
	}
	if !strings.Contains(persisted.Error, "[已隐藏]") {
		t.Fatalf("失败信息应包含脱敏占位，实际 %q", persisted.Error)
	}
	// 失败不得创建结果节点或媒体。
	graph, err := ReadRoomGraph(env.ctxA, env.roomID)
	if err != nil || len(graph.Nodes) != 0 {
		t.Fatalf("失败任务不得回填节点，实际 %d err=%v", len(graph.Nodes), err)
	}
	list, err := RoomMediaList(env.ctxA, env.roomID)
	if err != nil || list.Total != 0 {
		t.Fatalf("失败任务不得写入媒体，实际 %d err=%v", list.Total, err)
	}
}

// TestUploadSchoolTaskResultDoesNotOverwriteExistingNode 反例：结果回填只新增节点，
// 绝不把共享画布已有节点覆盖为本地旧快照。
func TestUploadSchoolTaskResultDoesNotOverwriteExistingNode(t *testing.T) {
	env := setupSchoolResultEnv(t, testPNGBytes(t), "image/png", nil)
	// 预置一个已有共享节点。
	applyOK(t, env.ctxA, env.roomID, envelope(env.roomID, "op-preexisting", contract.Operation{
		Type: contract.OpCreate,
		Node: &contract.Node{
			ID: "img_existing_node", Kind: contract.NodeKindImage, Title: "已有节点",
			Position: contract.Position{X: 1, Y: 2}, Width: 10, Height: 10, Version: 0,
			Metadata: map[string]any{"prompt": "旧快照"},
		},
	}))
	before, ok := roomNodeByID(t, env.roomID, "img_existing_node")
	if !ok {
		t.Fatal("预置节点应存在")
	}

	task := mustSubmit(t, env.ctxA, env, model.SchoolChannelQwen, "client-no-overwrite",
		model.SchoolTaskParams{Prompt: "新结果"})
	if _, ok := mustClaim(t, "executor-no-overwrite"); !ok {
		t.Fatal("应能领取任务")
	}
	if _, err := UploadSchoolTaskResult(context.Background(), "executor-no-overwrite", task.ID,
		SchoolResultInput{ResultURLs: []string{"https://example.invalid/result-0.png"}}); err != nil {
		t.Fatalf("结果回填失败: %v", err)
	}

	after, ok := roomNodeByID(t, env.roomID, "img_existing_node")
	if !ok {
		t.Fatal("回填不得删除已有节点")
	}
	if after.Version != before.Version || after.Metadata != before.Metadata || after.Title != before.Title {
		t.Fatalf("回填不得覆盖已有节点：before=%+v after=%+v", before, after)
	}
	graph, err := ReadRoomGraph(env.ctxA, env.roomID)
	if err != nil || len(graph.Nodes) != 2 {
		t.Fatalf("回填应新增节点且保留已有节点，实际 %d err=%v", len(graph.Nodes), err)
	}
}

// TestUploadSchoolTaskResultVideoChannel H3 渠道成功用例：生成视频节点并保留时长参数。
func TestUploadSchoolTaskResultVideoChannel(t *testing.T) {
	env := setupSchoolResultEnv(t, []byte("fake-mp4-bytes"), "video/mp4", nil)
	task := mustSubmit(t, env.ctxA, env, model.SchoolChannelH3, "client-result-video",
		model.SchoolTaskParams{Prompt: "推进镜头", ReferenceImages: []string{"img-a"}, VideoSeconds: 5})
	if _, ok := mustClaim(t, "executor-video"); !ok {
		t.Fatal("应能领取任务")
	}
	backfill, err := UploadSchoolTaskResult(context.Background(), "executor-video", task.ID,
		SchoolResultInput{ResultURLs: []string{"https://example.invalid/result-0.mp4"}})
	if err != nil {
		t.Fatalf("视频结果回填应成功，实际 %v", err)
	}
	if backfill.Node.Kind != string(contract.NodeKindVideo) {
		t.Fatalf("H3 渠道应生成视频节点，实际 %s", backfill.Node.Kind)
	}
	metadata := nodeMetadata(t, backfill.Node)
	if metadata["seconds"] != "5" {
		t.Fatalf("视频节点应保留时长参数，实际 %v", metadata["seconds"])
	}
}
