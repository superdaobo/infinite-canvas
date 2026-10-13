package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/service"
)

// 房间媒体分片上传 HTTP 路径测试（协作 v1，C04-03 #26）。
// 覆盖：完整分片流程（乱序）、Range 播放、附件下载、缺分片 409、超配额 413、非法类型 415、
// 非成员 404，以及小文件单请求路径回归。

type apiEnvelope[T any] struct {
	Code int    `json:"code"`
	Data T      `json:"data"`
	Msg  string `json:"msg"`
}

func decodeEnvelope[T any](t *testing.T, body []byte) apiEnvelope[T] {
	t.Helper()
	var env apiEnvelope[T]
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, string(body))
	}
	return env
}

func handlerTestDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// useSmallChunks 把分片大小压到 4 字节，让 16 字节测试 PNG 覆盖多分片场景。
func useSmallChunks(t *testing.T) {
	t.Helper()
	prev := service.RoomMediaChunkBytes
	service.RoomMediaChunkBytes = 4
	t.Cleanup(func() { service.RoomMediaChunkBytes = prev })
}

func createUploadSessionOverHTTP(t *testing.T, ctx context.Context, roomID, filename, contentType string, totalBytes int64, digest string) model.RoomMediaUploadView {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"filename": filename, "contentType": contentType, "totalBytes": totalBytes, "sha256": digest,
	})
	if err != nil {
		t.Fatalf("序列化请求失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload)).WithContext(ctx)
	rr := httptest.NewRecorder()
	CollabCreateRoomMediaUpload(rr, req, roomID)
	if rr.Code != http.StatusOK {
		t.Fatalf("创建会话应 200，实际 %d body=%s", rr.Code, rr.Body.String())
	}
	return decodeEnvelope[model.RoomMediaUploadView](t, rr.Body.Bytes()).Data
}

// TestCollabChunkedUploadOverHTTP 完整 HTTP 流程：创建会话 → 乱序上传分片 → 查询进度 → 完成 →
// Range 播放带 nosniff → 附件下载 → 重复完成幂等。
func TestCollabChunkedUploadOverHTTP(t *testing.T) {
	ctx, roomID, _ := setupCollabMediaHandler(t)
	useSmallChunks(t)
	data := testHandlerPNG()
	digest := handlerTestDigest(data)

	session := createUploadSessionOverHTTP(t, ctx, roomID, "big.png", "image/png", int64(len(data)), digest)
	if session.ChunkCount < 2 {
		t.Fatalf("测试应覆盖多分片，实际 chunkCount=%d", session.ChunkCount)
	}

	// 逆序上传并重复最后一片。
	order := make([]int, 0, session.ChunkCount+1)
	for i := session.ChunkCount - 1; i >= 0; i-- {
		order = append(order, i)
	}
	order = append(order, session.ChunkCount-1)
	for _, index := range order {
		start := int64(index) * session.ChunkBytes
		end := start + session.ChunkBytes
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(data[start:end])).WithContext(ctx)
		rr := httptest.NewRecorder()
		CollabUploadRoomMediaChunk(rr, req, roomID, session.ID, strconv.Itoa(index))
		if rr.Code != http.StatusOK {
			t.Fatalf("上传分片 %d 应 200，实际 %d body=%s", index, rr.Code, rr.Body.String())
		}
	}

	// 进度查询：满进度。
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	CollabRoomMediaUploadProgress(rr, req, roomID, session.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("查询进度应 200，实际 %d", rr.Code)
	}
	progress := decodeEnvelope[model.RoomMediaUploadView](t, rr.Body.Bytes()).Data
	if progress.ReceivedChunks != session.ChunkCount || progress.ReceivedBytes != int64(len(data)) {
		t.Fatalf("进度应满: %+v", progress)
	}

	// 完成。
	req = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	rr = httptest.NewRecorder()
	CollabCompleteRoomMediaUpload(rr, req, roomID, session.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("完成应 200，实际 %d body=%s", rr.Code, rr.Body.String())
	}
	media := decodeEnvelope[model.RoomMediaView](t, rr.Body.Bytes()).Data
	if media.ID != digest || media.Bytes != int64(len(data)) {
		t.Fatalf("完成媒体不符: %+v", media)
	}

	// Range 播放：206 + nosniff。
	req = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	req.Header.Set("Range", "bytes=0-3")
	rr = httptest.NewRecorder()
	CollabRoomMediaContent(rr, req, roomID, media.ID)
	if rr.Code != http.StatusPartialContent {
		t.Fatalf("Range 播放应 206，实际 %d", rr.Code)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if !bytes.Equal(rr.Body.Bytes(), data[:4]) {
		t.Fatalf("Range 内容不符: %x", rr.Body.Bytes())
	}

	// 附件下载：Content-Disposition attachment，内容完整。
	req = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rr = httptest.NewRecorder()
	CollabRoomMediaDownload(rr, req, roomID, media.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("下载应 200，实际 %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Fatalf("下载应带附件头，实际 %q", got)
	}
	if !bytes.Equal(rr.Body.Bytes(), data) {
		t.Fatalf("下载内容应与原始一致")
	}

	// 重复完成幂等。
	req = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	rr = httptest.NewRecorder()
	CollabCompleteRoomMediaUpload(rr, req, roomID, session.ID)
	if rr.Code != http.StatusOK || decodeEnvelope[model.RoomMediaView](t, rr.Body.Bytes()).Data.ID != digest {
		t.Fatalf("重复完成应幂等返回同一媒体，实际 %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestCollabChunkedUploadRejectsMissingChunkOverHTTP 缺分片时完成返回 409。
func TestCollabChunkedUploadRejectsMissingChunkOverHTTP(t *testing.T) {
	ctx, roomID, _ := setupCollabMediaHandler(t)
	useSmallChunks(t)
	data := testHandlerPNG()
	session := createUploadSessionOverHTTP(t, ctx, roomID, "missing.png", "image/png", int64(len(data)), handlerTestDigest(data))

	// 只上传第 0 片。
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(data[:session.ChunkBytes])).WithContext(ctx)
	rr := httptest.NewRecorder()
	CollabUploadRoomMediaChunk(rr, req, roomID, session.ID, "0")
	if rr.Code != http.StatusOK {
		t.Fatalf("上传分片应 200，实际 %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	rr = httptest.NewRecorder()
	CollabCompleteRoomMediaUpload(rr, req, roomID, session.ID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("缺分片完成应 409，实际 %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestCollabChunkedUploadRejectsOversizeAndTypeOverHTTP 超配额 413、非法类型 415。
func TestCollabChunkedUploadRejectsOversizeAndTypeOverHTTP(t *testing.T) {
	ctx, roomID, _ := setupCollabMediaHandler(t)
	useSmallChunks(t)

	prev := service.MaxRoomMediaChunkedBytes
	service.MaxRoomMediaChunkedBytes = 8
	t.Cleanup(func() { service.MaxRoomMediaChunkedBytes = prev })

	payload, _ := json.Marshal(map[string]any{"filename": "huge.png", "contentType": "image/png", "totalBytes": 16})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload)).WithContext(ctx)
	rr := httptest.NewRecorder()
	CollabCreateRoomMediaUpload(rr, req, roomID)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超配额创建会话应 413，实际 %d body=%s", rr.Code, rr.Body.String())
	}

	// 大小在配额内，专门验证类型白名单：非法类型应 415 而非 413。
	payload, _ = json.Marshal(map[string]any{"filename": "evil.svg", "contentType": "image/svg+xml", "totalBytes": 8})
	req = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload)).WithContext(ctx)
	rr = httptest.NewRecorder()
	CollabCreateRoomMediaUpload(rr, req, roomID)
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("非法类型创建会话应 415，实际 %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestCollabChunkedUploadNonMemberOverHTTP 非成员创建会话返回 404（不泄漏房间存在性）。
func TestCollabChunkedUploadNonMemberOverHTTP(t *testing.T) {
	_, roomID, _ := setupCollabMediaHandler(t)
	stranger := service.WithUser(context.Background(), model.AuthUser{ID: "not-a-member"})
	payload, _ := json.Marshal(map[string]any{"filename": "x.png", "contentType": "image/png", "totalBytes": 16})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload)).WithContext(stranger)
	rr := httptest.NewRecorder()
	CollabCreateRoomMediaUpload(rr, req, roomID)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("非成员创建会话应 404，实际 %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestCollabSingleRequestUploadOverHTTPStillWorks 回归：小文件仍走既有单请求 multipart 路径。
func TestCollabSingleRequestUploadOverHTTPStillWorks(t *testing.T) {
	ctx, roomID, _ := setupCollabMediaHandler(t)
	data := testHandlerPNG()
	digest := handlerTestDigest(data)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="file"; filename="small.png"`},
		"Content-Type":        {"image/png"},
	})
	if err != nil {
		t.Fatalf("构造 multipart 失败: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("写入 multipart 失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", &body).WithContext(ctx)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()
	CollabUploadRoomMedia(rr, req, roomID)
	if rr.Code != http.StatusOK {
		t.Fatalf("单请求上传应 200，实际 %d body=%s", rr.Code, rr.Body.String())
	}
	if got := decodeEnvelope[model.RoomMediaView](t, rr.Body.Bytes()).Data.ID; got != digest {
		t.Fatalf("单请求上传媒体 ID = %s, want %s", got, digest)
	}
}
