package handler

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
	"github.com/tigerowo/infinite-canvas/service"
)

// 房间媒体响应头加固测试（PR #47 P1 安全）：nosniff + 不内联主动内容。

// setupCollabMediaHandler 准备内存库、临时媒体目录与一个 owner 上下文。
func setupCollabMediaHandler(t *testing.T) (context.Context, string, string) {
	t.Helper()
	previous := config.Cfg
	t.Cleanup(func() { config.Cfg = previous })
	config.Cfg = config.Config{StorageDriver: "sqlite", DatabaseDSN: ":memory:", AILogDir: t.TempDir(), MediaDir: t.TempDir()}
	if _, err := repository.DB(); err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	userID := "media-handler-user"
	ctx := service.WithUser(context.Background(), model.AuthUser{ID: userID})
	room, err := service.CreateRoom(ctx, "媒体响应头测试房间")
	if err != nil {
		t.Fatalf("创建测试房间失败: %v", err)
	}
	return ctx, room.ID, userID
}

func testHandlerPNG() []byte {
	return []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13, 'I', 'H', 'D', 'R'}
}

// TestServeRoomMediaAddsNosniffForStoredPNG 已存合法 PNG 读取时必须带 nosniff 且可内联。
func TestServeRoomMediaAddsNosniffForStoredPNG(t *testing.T) {
	ctx, roomID, _ := setupCollabMediaHandler(t)
	media, err := service.UploadRoomMedia(ctx, roomID, "pic.png", "image/png", bytes.NewReader(testHandlerPNG()))
	if err != nil {
		t.Fatalf("上传 PNG 应成功，实际 %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	CollabRoomMediaContent(rr, req, roomID, media.ID)

	if rr.Code != http.StatusOK {
		t.Fatalf("读取已存 PNG 应 200，实际 %d", rr.Code)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rr.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", got)
	}
	if got := rr.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("安全图片不应强制附件，实际 Content-Disposition=%q", got)
	}
}

// TestServeRoomMediaForcesAttachmentForLegacySVG 历史遗留 SVG 记录读取时必须作为附件下载，
// 且使用通用二进制类型，绝不在同源内联渲染。
func TestServeRoomMediaForcesAttachmentForLegacySVG(t *testing.T) {
	ctx, roomID, userID := setupCollabMediaHandler(t)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	relPath := filepath.ToSlash(filepath.Join("legacy", "evil.svg"))
	absPath := filepath.Join(config.Cfg.MediaDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("创建历史媒体目录失败: %v", err)
	}
	if err := os.WriteFile(absPath, svg, 0o644); err != nil {
		t.Fatalf("写入历史媒体失败: %v", err)
	}
	now := time.Now().UTC()
	if _, err := repository.SaveRoomMedia(model.RoomMedia{
		RoomID: roomID, ID: "legacy-svg-1", Kind: model.RoomMediaKindImage, Filename: "evil.svg",
		MimeType: "image/svg+xml", Bytes: int64(len(svg)), StoragePath: relPath,
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入历史 SVG 记录失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	CollabRoomMediaContent(rr, req, roomID, "legacy-svg-1")

	if rr.Code != http.StatusOK {
		t.Fatalf("读取历史记录应 200，实际 %d", rr.Code)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("历史 SVG 应以 octet-stream 返回，实际 %q", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Fatalf("历史 SVG 必须强制附件下载，实际 Content-Disposition=%q", got)
	}
	if !bytes.Equal(rr.Body.Bytes(), svg) {
		t.Fatalf("附件下载内容应与存储一致")
	}
}

// TestCollabUploadRoomMediaRejectsSVGOverHTTP 完整 HTTP 路径：上传含脚本 SVG 必须 415。
func TestCollabUploadRoomMediaRejectsSVGOverHTTP(t *testing.T) {
	ctx, roomID, _ := setupCollabMediaHandler(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="file"; filename="evil.svg"`},
		"Content-Type":        {"image/svg+xml"},
	})
	if err != nil {
		t.Fatalf("构造 multipart 失败: %v", err)
	}
	if _, err := part.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)); err != nil {
		t.Fatalf("写入 multipart 失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", &body).WithContext(ctx)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()
	CollabUploadRoomMedia(rr, req, roomID)

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("上传 SVG 应 415，实际 %d body=%s", rr.Code, rr.Body.String())
	}
}
