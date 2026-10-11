package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
	"github.com/tigerowo/infinite-canvas/service"
)

// mediaTestEnv 是房间媒体 HTTP 集成测试环境：内存库 + 真实 HTTP 服务 + 四类账号。
type mediaTestEnv struct {
	server   *httptest.Server
	roomID   string
	owner    model.AuthSession
	editor   model.AuthSession
	viewer   model.AuthSession
	outsider model.AuthSession
}

func setupMediaTestEnv(t *testing.T) *mediaTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	config.Cfg = config.Config{
		StorageDriver: "sqlite",
		DatabaseDSN:   ":memory:",
		JWTSecret:     "c04-01-media-test-secret",
		AILogDir:      t.TempDir(),
		MediaDir:      t.TempDir(),
	}
	db, err := repository.DB()
	if err != nil {
		t.Fatalf("初始化测试库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	owner := registerWSUser(t, "media_owner")
	editor := registerWSUser(t, "media_editor")
	viewer := registerWSUser(t, "media_viewer")
	outsider := registerWSUser(t, "media_outsider")

	room, err := service.CreateRoom(wsUserContext(owner.User.ID), "房间媒体集成测试房间")
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	addWSMember(t, room.ID, editor.User.ID, model.RoomRoleEditor)
	addWSMember(t, room.ID, viewer.User.ID, model.RoomRoleViewer)

	engine := New()
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	return &mediaTestEnv{server: server, roomID: room.ID, owner: owner, editor: editor, viewer: viewer, outsider: outsider}
}

// pngBytes 返回一段最小可被 http.DetectContentType 识别为 image/png 的内容。
func pngBytes(size int) []byte {
	data := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	for len(data) < size {
		data = append(data, byte(len(data)%251))
	}
	return data
}

func mediaURL(roomID string, parts ...string) string {
	url := "/api/v1/collab/rooms/" + roomID + "/media"
	for _, part := range parts {
		url += "/" + part
	}
	return url
}

// uploadRoomMedia 以 multipart 上传一段媒体，返回原始响应与解析后的响应体。
func uploadRoomMedia(t *testing.T, env *mediaTestEnv, token, roomID, filename, contentType string, data []byte) (*http.Response, struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
	Msg  string          `json:"msg"`
}) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("构造 multipart 失败: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("写入 multipart 失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, env.server.URL+mediaURL(roomID), &buf)
	if err != nil {
		t.Fatalf("构造上传请求失败: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := env.server.Client().Do(req)
	if err != nil {
		t.Fatalf("上传请求失败: %v", err)
	}
	defer resp.Body.Close()
	var payload struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
		Msg  string          `json:"msg"`
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &payload)
	return resp, payload
}

// doMediaRequest 发起一次带可选令牌与 Range 的请求。
func doMediaRequest(t *testing.T, env *mediaTestEnv, method, url, token, rangeHeader string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, env.server.URL+url, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := env.server.Client().Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func decodeMediaView(t *testing.T, raw json.RawMessage) model.RoomMediaView {
	t.Helper()
	var view model.RoomMediaView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("解析媒体视图失败: %v raw=%s", err, string(raw))
	}
	return view
}

// TestCollabRoomMediaContract 覆盖房间媒体上传、播放、下载、授权与 R1 收敛的正反用例。
func TestCollabRoomMediaContract(t *testing.T) {
	env := setupMediaTestEnv(t)
	imageData := pngBytes(64)

	// --- 正例：owner 上传成功，返回摘要 ID 与授权地址 ---
	resp, payload := uploadRoomMedia(t, env, env.owner.Token, env.roomID, "first.png", "image/png", imageData)
	if resp.StatusCode != http.StatusOK || payload.Code != 0 {
		t.Fatalf("owner 上传应成功，实际 status=%d body=%s", resp.StatusCode, string(payload.Data))
	}
	image := decodeMediaView(t, payload.Data)
	if len(image.ID) != 64 || image.Kind != model.RoomMediaKindImage {
		t.Fatalf("上传返回的媒体标识/类别不正确: %+v", image)
	}
	if !strings.Contains(image.PlayURL, "/media/"+image.ID+"/content") {
		t.Fatalf("播放地址不正确: %s", image.PlayURL)
	}

	// --- 正例：editor 上传成功 ---
	videoData := []byte{0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70, 0x6D, 0x70, 0x34, 0x32, 1, 2, 3, 4}
	resp, payload = uploadRoomMedia(t, env, env.editor.Token, env.roomID, "clip.mp4", "video/mp4", videoData)
	if resp.StatusCode != http.StatusOK || payload.Code != 0 {
		t.Fatalf("editor 上传应成功，实际 status=%d", resp.StatusCode)
	}
	video := decodeMediaView(t, payload.Data)
	if video.Kind != model.RoomMediaKindVideo {
		t.Fatalf("视频类别判定错误: %+v", video)
	}

	// --- 去重：owner 重复上传相同内容得到同一 ID，列表数量不增加 ---
	resp, payload = uploadRoomMedia(t, env, env.owner.Token, env.roomID, "first-copy.png", "image/png", imageData)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("重复上传应成功，实际 status=%d", resp.StatusCode)
	}
	if duplicate := decodeMediaView(t, payload.Data); duplicate.ID != image.ID {
		t.Fatalf("相同内容应去重为同一 ID，got=%s want=%s", duplicate.ID, image.ID)
	}
	resp, body := doMediaRequest(t, env, http.MethodGet, mediaURL(env.roomID), env.viewer.Token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer 读取列表应成功，实际 status=%d body=%s", resp.StatusCode, string(body))
	}
	var listWrapper struct {
		Code int                 `json:"code"`
		Data model.RoomMediaList `json:"data"`
		Msg  string              `json:"msg"`
	}
	if err := json.Unmarshal(body, &listWrapper); err != nil {
		t.Fatalf("解析列表失败: %v body=%s", err, string(body))
	}
	list := listWrapper.Data
	if list.Total != 2 {
		t.Fatalf("去重后列表应为 2 条，实际 total=%d", list.Total)
	}

	// --- 反例：viewer 上传被拒（403）---
	resp, _ = uploadRoomMedia(t, env, env.viewer.Token, env.roomID, "viewer.png", "image/png", pngBytes(32))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer 上传应 403，实际 %d", resp.StatusCode)
	}

	// --- 反例：非成员上传 404，匿名上传 401 ---
	resp, _ = uploadRoomMedia(t, env, env.outsider.Token, env.roomID, "outsider.png", "image/png", pngBytes(32))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("非成员上传应 404，实际 %d", resp.StatusCode)
	}
	resp, _ = uploadRoomMedia(t, env, "", env.roomID, "anon.png", "image/png", pngBytes(32))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("匿名上传应 401，实际 %d", resp.StatusCode)
	}

	// --- 反例：非法类型被拒（415）---
	resp, _ = uploadRoomMedia(t, env, env.owner.Token, env.roomID, "note.txt", "text/plain", []byte("hello world"))
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("非法类型应 415，实际 %d", resp.StatusCode)
	}

	// --- 反例：超限被拒（413）---
	originalLimit := service.MaxRoomMediaBytes
	service.MaxRoomMediaBytes = 16
	resp, _ = uploadRoomMedia(t, env, env.owner.Token, env.roomID, "big.png", "image/png", pngBytes(64))
	service.MaxRoomMediaBytes = originalLimit
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限上传应 413，实际 %d", resp.StatusCode)
	}

	// --- 正例：成员播放返回完整内容，成员下载带附件头 ---
	resp, body = doMediaRequest(t, env, http.MethodGet, mediaURL(env.roomID, image.ID, "content"), env.viewer.Token, "")
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, imageData) {
		t.Fatalf("viewer 播放应返回完整内容，status=%d len=%d", resp.StatusCode, len(body))
	}
	resp, _ = doMediaRequest(t, env, http.MethodGet, mediaURL(env.roomID, image.ID, "download"), env.owner.Token, "")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("下载应带附件头，status=%d cd=%q", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}

	// --- 正例：成员 Range 播放返回 206 + Content-Range ---
	resp, body = doMediaRequest(t, env, http.MethodGet, mediaURL(env.roomID, image.ID, "content"), env.viewer.Token, "bytes=0-3")
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("Range 播放应 206，实际 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != fmt.Sprintf("bytes 0-3/%d", len(imageData)) {
		t.Fatalf("Content-Range 不正确: %q", got)
	}
	if !bytes.Equal(body, imageData[:4]) {
		t.Fatalf("Range 内容不正确: %v", body)
	}

	// --- 反例：非成员/匿名读取被拒 ---
	for _, tc := range []struct {
		name  string
		token string
		want  int
	}{
		{"非成员读取", env.outsider.Token, http.StatusNotFound},
		{"匿名读取", "", http.StatusUnauthorized},
	} {
		resp, _ = doMediaRequest(t, env, http.MethodGet, mediaURL(env.roomID, image.ID, "content"), tc.token, "")
		if resp.StatusCode != tc.want {
			t.Fatalf("%s 应 %d，实际 %d", tc.name, tc.want, resp.StatusCode)
		}
		resp, _ = doMediaRequest(t, env, http.MethodGet, mediaURL(env.roomID, image.ID), tc.token, "")
		if resp.StatusCode != tc.want {
			t.Fatalf("%s 元数据应 %d，实际 %d", tc.name, tc.want, resp.StatusCode)
		}
	}

	// --- R1 收敛：房间媒体 ID 不能经公开文件路径匿名读取 ---
	for _, path := range []string{"/api/files/" + image.ID, "/api/files/" + image.ID + "/content"} {
		resp, _ = doMediaRequest(t, env, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("公开路径 %s 读取房间媒体应 404，实际 %d", path, resp.StatusCode)
		}
	}

	// --- 删除：viewer 被拒（403），owner 成功，删除后读取 404 ---
	resp, _ = doMediaRequest(t, env, http.MethodDelete, mediaURL(env.roomID, image.ID), env.viewer.Token, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer 删除应 403，实际 %d", resp.StatusCode)
	}
	resp, _ = doMediaRequest(t, env, http.MethodDelete, mediaURL(env.roomID, image.ID), env.owner.Token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner 删除应 200，实际 %d", resp.StatusCode)
	}
	resp, _ = doMediaRequest(t, env, http.MethodGet, mediaURL(env.roomID, image.ID, "content"), env.owner.Token, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("删除后读取应 404，实际 %d", resp.StatusCode)
	}
}
