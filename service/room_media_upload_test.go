package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 房间媒体分片上传测试（协作 v1，C04-03 #26）。
// 覆盖：单请求路径回归、分片成功且内容正确、乱序/重复分片、缺分片拒绝、摘要不一致、
// 超配额、非法类型、非成员/只读成员拒绝、重复完成幂等、过期会话与清理、取消会话。

// roomMediaUploadTestEnv 在共享测试库上准备房间与临时媒体目录，并把分片大小压到 4 字节，
// 以便用一张 70 字节的合法 PNG 覆盖多分片场景。分片大小在用例结束时恢复。
func roomMediaUploadTestEnv(t *testing.T) (context.Context, string) {
	t.Helper()
	ctx, roomID := roomMediaTestEnv(t)
	prevChunk, prevMax := RoomMediaChunkBytes, MaxRoomMediaChunkedBytes
	t.Cleanup(func() {
		RoomMediaChunkBytes = prevChunk
		MaxRoomMediaChunkedBytes = prevMax
	})
	RoomMediaChunkBytes = 4
	return ctx, roomID
}

// sha256Hex 复用 service/storage.go 中的实现，对字节计算 SHA-256 十六进制摘要。

func chunkSlice(data []byte, index int, chunkBytes int64) []byte {
	start := int64(index) * chunkBytes
	end := start + chunkBytes
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	return data[start:end]
}

func uploadRoomMediaChunks(t *testing.T, ctx context.Context, roomID, sessionID string, data []byte, chunkBytes int64, order []int) {
	t.Helper()
	for _, index := range order {
		part := chunkSlice(data, index, chunkBytes)
		if _, err := UploadRoomMediaChunk(ctx, roomID, sessionID, index, bytes.NewReader(part)); err != nil {
			t.Fatalf("上传分片 %d 失败: %v", index, err)
		}
	}
}

func sequentialChunkOrder(count int) []int {
	order := make([]int, 0, count)
	for i := 0; i < count; i++ {
		order = append(order, i)
	}
	return order
}

func readRoomMediaBytes(t *testing.T, ctx context.Context, roomID, mediaID string) []byte {
	t.Helper()
	_, file, err := RoomMediaContent(ctx, roomID, mediaID)
	if err != nil {
		t.Fatalf("读取房间媒体失败: %v", err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("读取房间媒体内容失败: %v", err)
	}
	return data
}

// TestSingleRequestUploadPathUnchanged 回归：单请求路径行为不变，仍受自身大小上限约束。
func TestSingleRequestUploadPathUnchanged(t *testing.T) {
	ctx, roomID := roomMediaUploadTestEnv(t)
	data := testPNGBytes(t)
	view, err := UploadRoomMedia(ctx, roomID, "small.png", "image/png", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("小文件走单请求路径应成功，实际 %v", err)
	}
	if view.ID != sha256Hex(data) {
		t.Fatalf("单请求路径应按摘要命名，实际 %s", view.ID)
	}
	if !bytes.Equal(readRoomMediaBytes(t, ctx, roomID, view.ID), data) {
		t.Fatalf("单请求路径落盘内容应与上传一致")
	}

	// 单请求路径仍受 MaxRoomMediaBytes 约束（与分片上限相互独立）。
	prev := MaxRoomMediaBytes
	t.Cleanup(func() { MaxRoomMediaBytes = prev })
	MaxRoomMediaBytes = 8
	if _, err := UploadRoomMedia(ctx, roomID, "big.png", "image/png", bytes.NewReader(data)); !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("超过单请求上限应返回 ErrMediaTooLarge，实际 %v", err)
	}
}

// TestChunkedUploadSuccessAndContentCorrect 正向主用例：分片上传成功，摘要与内容一致。
func TestChunkedUploadSuccessAndContentCorrect(t *testing.T) {
	ctx, roomID := roomMediaUploadTestEnv(t)
	data := testPNGBytes(t)
	digest := sha256Hex(data)

	session, err := CreateRoomMediaUpload(ctx, roomID, "big.png", "image/png", int64(len(data)), digest)
	if err != nil {
		t.Fatalf("创建上传会话失败: %v", err)
	}
	wantCount := int((int64(len(data)) + RoomMediaChunkBytes - 1) / RoomMediaChunkBytes)
	if session.ChunkCount != wantCount || session.ChunkBytes != RoomMediaChunkBytes {
		t.Fatalf("会话分片参数不符: count=%d chunkBytes=%d", session.ChunkCount, session.ChunkBytes)
	}
	if session.Status != model.RoomMediaUploadStatusPending || session.TotalBytes != int64(len(data)) {
		t.Fatalf("会话初始状态不符: %+v", session)
	}

	uploadRoomMediaChunks(t, ctx, roomID, session.ID, data, RoomMediaChunkBytes, sequentialChunkOrder(session.ChunkCount))
	progress, err := RoomMediaUploadProgress(ctx, roomID, session.ID)
	if err != nil {
		t.Fatalf("查询进度失败: %v", err)
	}
	if progress.ReceivedChunks != session.ChunkCount || progress.ReceivedBytes != int64(len(data)) {
		t.Fatalf("上传完成后进度应为满: %+v", progress)
	}

	media, err := CompleteRoomMediaUpload(ctx, roomID, session.ID, "")
	if err != nil {
		t.Fatalf("完成分片上传失败: %v", err)
	}
	if media.ID != digest {
		t.Fatalf("媒体 ID 应等于内容摘要 %s，实际 %s", digest, media.ID)
	}
	if media.Bytes != int64(len(data)) {
		t.Fatalf("媒体大小 = %d, want %d", media.Bytes, len(data))
	}
	if !bytes.Equal(readRoomMediaBytes(t, ctx, roomID, media.ID), data) {
		t.Fatalf("分片合并后内容应与原始数据一致")
	}
	if _, found, err := repository.FindRoomMedia(roomID, digest); err != nil || !found {
		t.Fatalf("完成登记后应存在媒体索引: found=%v err=%v", found, err)
	}
}

// TestChunkedUploadOutOfOrderAndDuplicateChunks 乱序 + 重复分片仍能正确合并。
func TestChunkedUploadOutOfOrderAndDuplicateChunks(t *testing.T) {
	ctx, roomID := roomMediaUploadTestEnv(t)
	data := testPNGBytes(t)
	digest := sha256Hex(data)
	session, err := CreateRoomMediaUpload(ctx, roomID, "out.png", "image/png", int64(len(data)), digest)
	if err != nil {
		t.Fatalf("创建上传会话失败: %v", err)
	}

	// 逆序上传，并重复上传第 0 与最后一个分片。
	order := make([]int, 0, session.ChunkCount+2)
	for i := session.ChunkCount - 1; i >= 0; i-- {
		order = append(order, i)
	}
	order = append(order, 0, session.ChunkCount-1)
	uploadRoomMediaChunks(t, ctx, roomID, session.ID, data, RoomMediaChunkBytes, order)

	media, err := CompleteRoomMediaUpload(ctx, roomID, session.ID, "")
	if err != nil {
		t.Fatalf("乱序/重复分片后完成应成功: %v", err)
	}
	if media.ID != digest || !bytes.Equal(readRoomMediaBytes(t, ctx, roomID, media.ID), data) {
		t.Fatalf("乱序/重复分片合并结果不正确")
	}
}

// TestChunkedUploadRejectsMissingChunkThenRetrySucceeds 缺分片时完成被拒，补齐后可重试成功。
func TestChunkedUploadRejectsMissingChunkThenRetrySucceeds(t *testing.T) {
	ctx, roomID := roomMediaUploadTestEnv(t)
	data := testPNGBytes(t)
	digest := sha256Hex(data)
	session, err := CreateRoomMediaUpload(ctx, roomID, "missing.png", "image/png", int64(len(data)), digest)
	if err != nil {
		t.Fatalf("创建上传会话失败: %v", err)
	}
	// 故意漏掉第 1 片。
	order := sequentialChunkOrder(session.ChunkCount)
	order = append(order[:1], order[2:]...)
	uploadRoomMediaChunks(t, ctx, roomID, session.ID, data, RoomMediaChunkBytes, order)

	if _, err := CompleteRoomMediaUpload(ctx, roomID, session.ID, ""); !errors.Is(err, ErrMediaUploadIncomplete) {
		t.Fatalf("缺分片完成应返回 ErrMediaUploadIncomplete，实际 %v", err)
	}
	// 拒绝时绝不登记媒体。
	if _, found, err := repository.FindRoomMedia(roomID, digest); err != nil || found {
		t.Fatalf("缺分片不得登记媒体: found=%v err=%v", found, err)
	}
	// 会话回退为 pending，补齐缺片后重试成功。
	progress, err := RoomMediaUploadProgress(ctx, roomID, session.ID)
	if err != nil {
		t.Fatalf("查询进度失败: %v", err)
	}
	if progress.Status != model.RoomMediaUploadStatusPending {
		t.Fatalf("失败完成应回退为 pending，实际 %s", progress.Status)
	}
	uploadRoomMediaChunks(t, ctx, roomID, session.ID, data, RoomMediaChunkBytes, []int{1})
	if _, err := CompleteRoomMediaUpload(ctx, roomID, session.ID, ""); err != nil {
		t.Fatalf("补齐分片后完成应成功: %v", err)
	}
}

// TestChunkedUploadRejectsDigestMismatch 摘要不一致时完成被拒，修正摘要后可成功。
func TestChunkedUploadRejectsDigestMismatch(t *testing.T) {
	ctx, roomID := roomMediaUploadTestEnv(t)
	data := testPNGBytes(t)
	wrongDigest := sha256Hex([]byte("not-the-real-content"))
	session, err := CreateRoomMediaUpload(ctx, roomID, "bad.png", "image/png", int64(len(data)), wrongDigest)
	if err != nil {
		t.Fatalf("创建上传会话失败: %v", err)
	}
	uploadRoomMediaChunks(t, ctx, roomID, session.ID, data, RoomMediaChunkBytes, sequentialChunkOrder(session.ChunkCount))

	if _, err := CompleteRoomMediaUpload(ctx, roomID, session.ID, ""); !errors.Is(err, ErrMediaDigestMismatch) {
		t.Fatalf("摘要不一致应返回 ErrMediaDigestMismatch，实际 %v", err)
	}
	if _, found, err := repository.FindRoomMedia(roomID, wrongDigest); err != nil || found {
		t.Fatalf("摘要不一致不得登记媒体: found=%v err=%v", found, err)
	}
	// 完成请求可覆盖摘要：用正确摘要重试成功。
	if _, err := CompleteRoomMediaUpload(ctx, roomID, session.ID, sha256Hex(data)); err != nil {
		t.Fatalf("以正确摘要重试完成应成功: %v", err)
	}
}

// TestChunkedUploadRejectsOversizeTotal 超过分片上传总大小上限在创建会话时被拒。
func TestChunkedUploadRejectsOversizeTotal(t *testing.T) {
	ctx, roomID := roomMediaUploadTestEnv(t)
	MaxRoomMediaChunkedBytes = 10
	if _, err := CreateRoomMediaUpload(ctx, roomID, "huge.png", "image/png", 70, ""); !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("超过分片上传上限应返回 ErrMediaTooLarge，实际 %v", err)
	}
}

// TestChunkedUploadRejectsInvalidInputs 非法类型/参数在创建会话时被拒；内容与声明不符在完成时被拒。
func TestChunkedUploadRejectsInvalidInputs(t *testing.T) {
	ctx, roomID := roomMediaUploadTestEnv(t)
	cases := []struct {
		name        string
		contentType string
		totalBytes  int64
		digest      string
		want        error
	}{
		{"主动内容 SVG", "image/svg+xml", 70, "", ErrMediaUnsupported},
		{"白名单外 PDF", "application/pdf", 70, "", ErrMediaUnsupported},
		{"非正大小", "image/png", 0, "", ErrMediaInvalid},
		{"非法摘要长度", "image/png", 70, "abcd", ErrMediaInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CreateRoomMediaUpload(ctx, roomID, "x.bin", tc.contentType, tc.totalBytes, tc.digest); !errors.Is(err, tc.want) {
				t.Fatalf("应返回 %v，实际 %v", tc.want, err)
			}
		})
	}

	// 声明 image/png 但实际内容是 HTML：完成时由内容嗅探拒绝，绝不落盘。
	html := []byte(`<html><body><script>alert(1)</script></body></html>`)
	session, err := CreateRoomMediaUpload(ctx, roomID, "fake.png", "image/png", int64(len(html)), sha256Hex(html))
	if err != nil {
		t.Fatalf("创建上传会话失败: %v", err)
	}
	uploadRoomMediaChunks(t, ctx, roomID, session.ID, html, RoomMediaChunkBytes, sequentialChunkOrder(session.ChunkCount))
	if _, err := CompleteRoomMediaUpload(ctx, roomID, session.ID, ""); !errors.Is(err, ErrMediaUnsupported) {
		t.Fatalf("伪装图片完成应返回 ErrMediaUnsupported，实际 %v", err)
	}
}

// TestChunkedUploadRejectsInvalidChunkIndexOrSize 分片序号越界、负数与大小不符被拒。
func TestChunkedUploadRejectsInvalidChunkIndexOrSize(t *testing.T) {
	ctx, roomID := roomMediaUploadTestEnv(t)
	data := testPNGBytes(t)
	session, err := CreateRoomMediaUpload(ctx, roomID, "idx.png", "image/png", int64(len(data)), sha256Hex(data))
	if err != nil {
		t.Fatalf("创建上传会话失败: %v", err)
	}
	if _, err := UploadRoomMediaChunk(ctx, roomID, session.ID, -1, bytes.NewReader([]byte{1, 2, 3, 4})); !errors.Is(err, ErrMediaInvalid) {
		t.Fatalf("负数序号应返回 ErrMediaInvalid，实际 %v", err)
	}
	if _, err := UploadRoomMediaChunk(ctx, roomID, session.ID, session.ChunkCount, bytes.NewReader([]byte{1, 2, 3, 4})); !errors.Is(err, ErrMediaInvalid) {
		t.Fatalf("越界序号应返回 ErrMediaInvalid，实际 %v", err)
	}
	// 非末片必须精确等于分片大小。
	if _, err := UploadRoomMediaChunk(ctx, roomID, session.ID, 0, bytes.NewReader([]byte{1, 2, 3})); !errors.Is(err, ErrMediaInvalid) {
		t.Fatalf("分片大小不符应返回 ErrMediaInvalid，实际 %v", err)
	}
}

// TestChunkedUploadPermissionBoundaries 权限负例：viewer 不可写、非成员不可见、非创建者不可操作会话。
func TestChunkedUploadPermissionBoundaries(t *testing.T) {
	ctx, roomID := roomMediaUploadTestEnv(t)
	data := testPNGBytes(t)

	viewerID := newID("viewer")
	editorID := newID("editor")
	strangerID := newID("stranger")
	now := time.Now().UTC()
	if err := repository.SaveRoomMember(model.RoomMember{RoomID: roomID, UserID: viewerID, Role: model.RoomRoleViewer, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("写入 viewer 成员失败: %v", err)
	}
	if err := repository.SaveRoomMember(model.RoomMember{RoomID: roomID, UserID: editorID, Role: model.RoomRoleEditor, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("写入 editor 成员失败: %v", err)
	}

	if _, err := CreateRoomMediaUpload(collabContext(viewerID), roomID, "v.png", "image/png", int64(len(data)), ""); !errors.Is(err, ErrRoomForbidden) {
		t.Fatalf("viewer 创建会话应返回 ErrRoomForbidden，实际 %v", err)
	}
	if _, err := CreateRoomMediaUpload(collabContext(strangerID), roomID, "s.png", "image/png", int64(len(data)), ""); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("非成员创建会话应返回 ErrRoomNotFound，实际 %v", err)
	}

	// editor 可创建会话，但只能操作自己的会话；owner 的会话对它不可见。
	editorSession, err := CreateRoomMediaUpload(collabContext(editorID), roomID, "e.png", "image/png", int64(len(data)), "")
	if err != nil {
		t.Fatalf("editor 创建会话应成功: %v", err)
	}
	ownerSession, err := CreateRoomMediaUpload(ctx, roomID, "o.png", "image/png", int64(len(data)), "")
	if err != nil {
		t.Fatalf("owner 创建会话应成功: %v", err)
	}
	if _, err := UploadRoomMediaChunk(collabContext(editorID), roomID, ownerSession.ID, 0, bytes.NewReader([]byte{1, 2, 3, 4})); !errors.Is(err, ErrMediaUploadNotFound) {
		t.Fatalf("非创建者上传他人会话分片应返回 ErrMediaUploadNotFound，实际 %v", err)
	}
	if _, err := UploadRoomMediaChunk(collabContext(editorID), roomID, editorSession.ID, 0, bytes.NewReader([]byte{1, 2, 3, 4})); err != nil {
		t.Fatalf("创建者上传自己的会话分片应成功: %v", err)
	}
	// 任意成员可查询进度，非成员不可。
	if _, err := RoomMediaUploadProgress(collabContext(viewerID), roomID, editorSession.ID); err != nil {
		t.Fatalf("viewer 查询进度应成功: %v", err)
	}
	if _, err := RoomMediaUploadProgress(collabContext(strangerID), roomID, editorSession.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("非成员查询进度应返回 ErrRoomNotFound，实际 %v", err)
	}
}

// TestChunkedUploadConcurrentChunksAndComplete 并发乱序上传分片 + 并发完成请求：
// 不产生损坏文件，成功完成者返回同一媒体，其余为可重试冲突。
func TestChunkedUploadConcurrentChunksAndComplete(t *testing.T) {
	ctx, roomID := roomMediaUploadTestEnv(t)
	data := testPNGBytes(t)
	digest := sha256Hex(data)
	session, err := CreateRoomMediaUpload(ctx, roomID, "conc.png", "image/png", int64(len(data)), digest)
	if err != nil {
		t.Fatalf("创建上传会话失败: %v", err)
	}

	// 并发上传全部分片（乱序提交）。
	var wg sync.WaitGroup
	errs := make(chan error, session.ChunkCount)
	for i := 0; i < session.ChunkCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			part := chunkSlice(data, index, RoomMediaChunkBytes)
			if _, err := UploadRoomMediaChunk(ctx, roomID, session.ID, index, bytes.NewReader(part)); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发上传分片失败: %v", err)
	}

	// 并发完成请求。
	const completions = 4
	type result struct {
		view model.RoomMediaView
		err  error
	}
	results := make(chan result, completions)
	for i := 0; i < completions; i++ {
		go func() {
			view, err := CompleteRoomMediaUpload(ctx, roomID, session.ID, "")
			results <- result{view: view, err: err}
		}()
	}
	succeeded := 0
	for i := 0; i < completions; i++ {
		res := <-results
		switch {
		case res.err == nil:
			succeeded++
			if res.view.ID != digest {
				t.Fatalf("并发完成返回的媒体 ID = %s, want %s", res.view.ID, digest)
			}
		case errors.Is(res.err, ErrMediaUploadConflict):
			// 另一个完成请求已抢占，属预期。
		default:
			t.Fatalf("并发完成出现非预期错误: %v", res.err)
		}
	}
	if succeeded < 1 {
		t.Fatalf("并发完成应至少有一个成功")
	}
	if !bytes.Equal(readRoomMediaBytes(t, ctx, roomID, digest), data) {
		t.Fatalf("并发场景下合并内容损坏")
	}
}

// TestChunkedUploadCompleteIdempotent 重复完成幂等；完成后再上传分片被拒。
func TestChunkedUploadCompleteIdempotent(t *testing.T) {
	ctx, roomID := roomMediaUploadTestEnv(t)
	data := testPNGBytes(t)
	session, err := CreateRoomMediaUpload(ctx, roomID, "idem.png", "image/png", int64(len(data)), sha256Hex(data))
	if err != nil {
		t.Fatalf("创建上传会话失败: %v", err)
	}
	uploadRoomMediaChunks(t, ctx, roomID, session.ID, data, RoomMediaChunkBytes, sequentialChunkOrder(session.ChunkCount))

	first, err := CompleteRoomMediaUpload(ctx, roomID, session.ID, "")
	if err != nil {
		t.Fatalf("首次完成失败: %v", err)
	}
	second, err := CompleteRoomMediaUpload(ctx, roomID, session.ID, "")
	if err != nil {
		t.Fatalf("重复完成应幂等成功，实际 %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("重复完成应返回同一媒体: %s vs %s", first.ID, second.ID)
	}
	if _, err := UploadRoomMediaChunk(ctx, roomID, session.ID, 0, bytes.NewReader([]byte{1, 2, 3, 4})); !errors.Is(err, ErrMediaUploadState) {
		t.Fatalf("完成后上传分片应返回 ErrMediaUploadState，实际 %v", err)
	}
}

// TestChunkedUploadExpiredSessionAndCleanup 过期会话拒绝读写，且可被清理任务回收。
func TestChunkedUploadExpiredSessionAndCleanup(t *testing.T) {
	ctx, roomID := roomMediaUploadTestEnv(t)
	data := testPNGBytes(t)
	session, err := CreateRoomMediaUpload(ctx, roomID, "exp.png", "image/png", int64(len(data)), sha256Hex(data))
	if err != nil {
		t.Fatalf("创建上传会话失败: %v", err)
	}
	// 手动把会话置为过期。
	stored, found, err := repository.FindRoomMediaUpload(session.ID)
	if err != nil || !found {
		t.Fatalf("读取会话失败: found=%v err=%v", found, err)
	}
	stored.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if _, err := repository.SaveRoomMediaUpload(stored); err != nil {
		t.Fatalf("置为过期失败: %v", err)
	}

	if _, err := UploadRoomMediaChunk(ctx, roomID, session.ID, 0, bytes.NewReader(chunkSlice(data, 0, RoomMediaChunkBytes))); !errors.Is(err, ErrMediaUploadExpired) {
		t.Fatalf("过期会话上传分片应返回 ErrMediaUploadExpired，实际 %v", err)
	}
	if _, err := CompleteRoomMediaUpload(ctx, roomID, session.ID, ""); !errors.Is(err, ErrMediaUploadExpired) {
		t.Fatalf("过期会话完成应返回 ErrMediaUploadExpired，实际 %v", err)
	}

	removed, err := CleanupExpiredRoomMediaUploads(time.Now().UTC())
	if err != nil {
		t.Fatalf("清理过期会话失败: %v", err)
	}
	if removed < 1 {
		t.Fatalf("应至少清理 1 个过期会话，实际 %d", removed)
	}
	if _, found, _ := repository.FindRoomMediaUpload(session.ID); found {
		t.Fatalf("过期会话应已从库中删除")
	}
	if _, err := os.Stat(roomMediaUploadDir(session.ID)); !os.IsNotExist(err) {
		t.Fatalf("过期会话临时目录应已回收，stat err=%v", err)
	}
}

// TestChunkedUploadAbort 取消会话会删除记录与临时分片；非创建者不可取消。
func TestChunkedUploadAbort(t *testing.T) {
	ctx, roomID := roomMediaUploadTestEnv(t)
	data := testPNGBytes(t)
	session, err := CreateRoomMediaUpload(ctx, roomID, "abort.png", "image/png", int64(len(data)), sha256Hex(data))
	if err != nil {
		t.Fatalf("创建上传会话失败: %v", err)
	}
	if _, err := UploadRoomMediaChunk(ctx, roomID, session.ID, 0, bytes.NewReader(chunkSlice(data, 0, RoomMediaChunkBytes))); err != nil {
		t.Fatalf("上传分片失败: %v", err)
	}

	otherID := newID("other")
	now := time.Now().UTC()
	if err := repository.SaveRoomMember(model.RoomMember{RoomID: roomID, UserID: otherID, Role: model.RoomRoleEditor, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("写入成员失败: %v", err)
	}
	if err := AbortRoomMediaUpload(collabContext(otherID), roomID, session.ID); !errors.Is(err, ErrMediaUploadNotFound) {
		t.Fatalf("非创建者取消应返回 ErrMediaUploadNotFound，实际 %v", err)
	}
	if err := AbortRoomMediaUpload(ctx, roomID, session.ID); err != nil {
		t.Fatalf("创建者取消会话应成功: %v", err)
	}
	if _, found, _ := repository.FindRoomMediaUpload(session.ID); found {
		t.Fatalf("取消后会话应已删除")
	}
	if _, err := os.Stat(roomMediaUploadDir(session.ID)); !os.IsNotExist(err) {
		t.Fatalf("取消后临时目录应已回收，stat err=%v", err)
	}
}
