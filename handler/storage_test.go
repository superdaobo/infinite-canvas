package handler

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/service"
)

// 公开文件读取与图片代理的响应头加固测试（C05-02 安全审计 F1/F2）：
// 主动内容（SVG/HTML/XML/JS）绝不以可执行/可内联类型返回。

// TestDownloadResponseContentTypeNeutralizesActiveContent 覆盖类型收敛判定：
// 空类型与主动内容降级为通用二进制并要求附件下载，安全类型原样内联。
func TestDownloadResponseContentTypeNeutralizesActiveContent(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantType   string
		wantInline bool
	}{
		{"含脚本 SVG", "image/svg+xml", "application/octet-stream", false},
		{"SVG 带 charset 参数", "image/svg+xml; charset=utf-8", "application/octet-stream", false},
		{"HTML", "text/html", "application/octet-stream", false},
		{"XHTML", "application/xhtml+xml", "application/octet-stream", false},
		{"XML", "application/xml", "application/octet-stream", false},
		{"JavaScript", "application/javascript", "application/octet-stream", false},
		{"空类型", "", "application/octet-stream", false},
		{"PNG", "image/png", "image/png", true},
		{"JPEG", "image/jpeg", "image/jpeg", true},
		{"MP4", "video/mp4", "video/mp4", true},
		{"MP3", "audio/mpeg", "audio/mpeg", true},
		{"PDF", "application/pdf", "application/pdf", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotType, gotInline := downloadResponseContentType(tc.in)
			if gotType != tc.wantType || gotInline != tc.wantInline {
				t.Fatalf("downloadResponseContentType(%q) = (%q,%v)，期望 (%q,%v)",
					tc.in, gotType, gotInline, tc.wantType, tc.wantInline)
			}
		})
	}
}

// TestProxyImageNeutralizesActiveUpstreamContent 验证图片代理不反射上游主动内容：
// 远端 text/html 或 image/svg+xml 必须降级为 octet-stream + 附件 + nosniff，
// 安全图片类型保持内联且带 nosniff。
func TestProxyImageNeutralizesActiveUpstreamContent(t *testing.T) {
	payload := []byte("<html><script>alert(document.domain)</script></html>")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", r.URL.Query().Get("ct"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	// 测试用回环上游：临时替换出网客户端（生产默认走 SSRF 白名单客户端，禁止回环地址）。
	original := proxyImageHTTPClient
	proxyImageHTTPClient = func() *http.Client { return upstream.Client() }
	t.Cleanup(func() { proxyImageHTTPClient = original })

	call := func(contentType string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		target := upstream.URL + "/asset?ct=" + url.QueryEscape(contentType)
		req := httptest.NewRequest(http.MethodGet, "/api/proxy-image?url="+url.QueryEscape(target), nil)
		ProxyImage(rr, req)
		return rr
	}

	for _, active := range []string{"text/html", "image/svg+xml"} {
		rr := call(active)
		if rr.Code != http.StatusOK {
			t.Fatalf("上游 %s 代理应 200，实际 %d", active, rr.Code)
		}
		if got := rr.Header().Get("Content-Type"); got != "application/octet-stream" {
			t.Fatalf("上游 %s 应降级为 octet-stream，实际 %q", active, got)
		}
		if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("上游 %s 应带 nosniff，实际 %q", active, got)
		}
		if got := rr.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
			t.Fatalf("上游 %s 应强制附件下载，实际 Content-Disposition=%q", active, got)
		}
		if !bytes.Equal(rr.Body.Bytes(), payload) {
			t.Fatalf("上游 %s 附件内容应与上游一致", active)
		}
	}

	rr := call("image/png")
	if rr.Code != http.StatusOK {
		t.Fatalf("安全图片代理应 200，实际 %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("安全图片应保留 image/png，实际 %q", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("安全图片也应带 nosniff，实际 %q", got)
	}
	if got := rr.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("安全图片不应强制附件，实际 %q", got)
	}
}

// TestFileContentNeutralizesActiveStoredContent 验证公开文件路径不内联主动内容：
// 存储对象声明为 text/html 时，响应必须降级为 octet-stream + 附件 + nosniff。
func TestFileContentNeutralizesActiveStoredContent(t *testing.T) {
	// 初始化内存库（AuthorizeFileRead 需要 DB）并隔离配置。
	setupCollabMediaHandler(t)

	body := []byte("<html><script>alert(1)</script></html>")
	original := downloadStorageObject
	downloadStorageObject = func(id, rangeHeader string) (service.DownloadedStorageObject, error) {
		return service.DownloadedStorageObject{
			Object:        model.StorageObject{ID: id, ObjectKey: "u/evil.html", MimeType: "text/html", Bytes: int64(len(body))},
			Stream:        io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
		}, nil
	}
	t.Cleanup(func() { downloadStorageObject = original })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/files/obj_evil/content", nil)
	FileContent(rr, req, "obj_evil")

	if rr.Code != http.StatusOK {
		t.Fatalf("读取存储对象应 200，实际 %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("主动内容应降级为 octet-stream，实际 %q", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("应带 nosniff，实际 %q", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Fatalf("主动内容应强制附件下载，实际 Content-Disposition=%q", got)
	}
	if !bytes.Equal(rr.Body.Bytes(), body) {
		t.Fatalf("附件下载内容应与存储一致")
	}
}
