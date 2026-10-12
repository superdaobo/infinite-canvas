package service

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
)

// 房间媒体主动内容（SVG/HTML）XSS 加固测试（PR #47 P1 安全）。

// svgWithScriptBytes 返回含脚本的 SVG 主动内容。
func svgWithScriptBytes() []byte {
	return []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
}

// testPNGBytes 见 school_result_test.go，这里复用同一张 1x1 PNG。

func testWebPBytes() []byte {
	raw := []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")
	return append(raw, make([]byte, 32)...)
}

func testWAVBytes() []byte {
	raw := []byte("RIFF\x24\x00\x00\x00WAVEfmt ")
	return append(raw, make([]byte, 32)...)
}

func testMP3Bytes() []byte {
	raw := []byte("ID3\x03\x00\x00\x00\x00\x00\x00")
	return append(raw, make([]byte, 32)...)
}

// roomMediaTestEnv 准备内存库、临时媒体目录与一个 owner 上下文。
func roomMediaTestEnv(t *testing.T) (context.Context, string) {
	t.Helper()
	setupCollabDB(t, "sqlite", ":memory:")
	config.Cfg.MediaDir = t.TempDir()
	ctx := collabContext(newID("media-user"))
	room, err := CreateRoom(ctx, "媒体安全测试房间")
	if err != nil {
		t.Fatalf("创建测试房间失败: %v", err)
	}
	return ctx, room.ID
}

// TestUploadRoomMediaRejectsActiveContent 覆盖负向用例：主动内容与白名单外类型一律 415 语义。
func TestUploadRoomMediaRejectsActiveContent(t *testing.T) {
	ctx, roomID := roomMediaTestEnv(t)
	html := []byte(`<html><body><script>alert(document.cookie)</script></body></html>`)
	xmlSVG := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	cases := []struct {
		name     string
		declared string
		data     []byte
	}{
		{"含脚本 SVG 声明 image/svg+xml", "image/svg+xml", svgWithScriptBytes()},
		{"XML 头 SVG 声明 image/svg+xml", "image/svg+xml", xmlSVG},
		{"HTML 声明 text/html", "text/html", html},
		{"XHTML 声明 application/xhtml+xml", "application/xhtml+xml", html},
		{"XML 声明 text/xml", "text/xml", xmlSVG},
		{"JS 声明 application/javascript", "application/javascript", []byte(`alert(1)`)},
		{"白名单外 image/bmp", "image/bmp", []byte("BM\x00\x00\x00\x00")},
		{"白名单外 application/pdf", "application/pdf", []byte("%PDF-1.7\n")},
		{"空声明且内容不可嗅探", "", svgWithScriptBytes()},
		{"伪装图片：声明 image/png 内容为 HTML", "image/png", html},
		{"伪装图片：声明 image/png 内容为 SVG", "image/png", svgWithScriptBytes()},
		{"伪装图片：声明 image/gif 内容为文本", "image/gif", []byte("not-a-gif-just-text")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := UploadRoomMedia(ctx, roomID, "evil.svg", tc.declared, bytes.NewReader(tc.data))
			if !errors.Is(err, ErrMediaUnsupported) {
				t.Fatalf("应返回 ErrMediaUnsupported，实际 %v", err)
			}
		})
	}
}

// TestUploadRoomMediaAcceptsWhitelistedTypes 覆盖正向用例：白名单类型按内容校验后落库，
// 且落库类型是校验通过的类型而非回显客户端原始声明值。
func TestUploadRoomMediaAcceptsWhitelistedTypes(t *testing.T) {
	ctx, roomID := roomMediaTestEnv(t)
	cases := []struct {
		name         string
		declared     string
		data         []byte
		wantKind     model.RoomMediaKind
		wantMimeType string
	}{
		{"PNG", "image/png", testPNGBytes(t), model.RoomMediaKindImage, "image/png"},
		{"PNG 带参数声明", "image/png; charset=binary", testPNGBytes(t), model.RoomMediaKindImage, "image/png"},
		{"WebP", "image/webp", testWebPBytes(), model.RoomMediaKindImage, "image/webp"},
		{"MP4 声明（容器不可嗅探，信任白名单声明）", "video/mp4", []byte("fake-mp4-bytes"), model.RoomMediaKindVideo, "video/mp4"},
		{"WebM 声明", "video/webm", []byte("\x1a\x45\xdf\xa3fake-webm"), model.RoomMediaKindVideo, "video/webm"},
		{"MP3", "audio/mpeg", testMP3Bytes(), model.RoomMediaKindAudio, "audio/mpeg"},
		{"WAV", "audio/wav", testWAVBytes(), model.RoomMediaKindAudio, "audio/wav"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view, err := UploadRoomMedia(ctx, roomID, "ok.bin", tc.declared, bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("白名单类型应上传成功，实际 %v", err)
			}
			if view.Kind != tc.wantKind {
				t.Fatalf("Kind = %s, want %s", view.Kind, tc.wantKind)
			}
			if view.MimeType != tc.wantMimeType {
				t.Fatalf("落库 MimeType = %q, want %q", view.MimeType, tc.wantMimeType)
			}
		})
	}
}

// TestRoomMediaInlineSafe 覆盖响应类型判定：主动内容与未知类型一律非内联安全。
func TestRoomMediaInlineSafe(t *testing.T) {
	cases := map[string]bool{
		"image/png":                    true,
		"image/jpeg":                   true,
		"image/webp":                   true,
		"image/gif":                    true,
		"video/mp4":                    true,
		"video/webm":                   true,
		"audio/mpeg":                   true,
		"audio/wav":                    true,
		"audio/x-wav":                  true,
		"audio/ogg":                    true,
		"audio/mp4":                    true,
		"audio/webm":                   true,
		"image/svg+xml":                false,
		"image/svg+xml; charset=utf-8": false,
		"text/html":                    false,
		"text/xml":                     false,
		"application/xhtml+xml":        false,
		"application/octet-stream":     false,
		"":                             false,
	}
	for mimeType, want := range cases {
		if got := RoomMediaInlineSafe(mimeType); got != want {
			t.Fatalf("RoomMediaInlineSafe(%q) = %v, want %v", mimeType, got, want)
		}
	}
	if got := RoomMediaResponseContentType("image/svg+xml"); got != "application/octet-stream" {
		t.Fatalf("历史 SVG 响应类型应回退为 octet-stream，实际 %q", got)
	}
	if got := RoomMediaResponseContentType("image/png"); got != "image/png" {
		t.Fatalf("PNG 响应类型应保持 image/png，实际 %q", got)
	}
}
