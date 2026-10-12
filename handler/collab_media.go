package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/tigerowo/infinite-canvas/service"
)

// CollabUploadRoomMedia 上传房间共享媒体（owner/editor 可上传）。
// 校验媒体类型与大小，内容按摘要命名去重；响应为 {code,data,msg}。
func CollabUploadRoomMedia(w http.ResponseWriter, r *http.Request, roomID string) {
	// 提前限制请求体大小，避免超大上传先落盘；multipart 边界等开销留 1MB 余量。
	r.Body = http.MaxBytesReader(w, r.Body, service.MaxRoomMediaBytes+(1<<20))
	file, header, err := r.FormFile("file")
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			FailWithStatus(w, http.StatusRequestEntityTooLarge, service.ErrMediaTooLarge.Error())
			return
		}
		FailWithStatus(w, http.StatusBadRequest, service.ErrMediaInvalid.Error())
		return
	}
	defer file.Close()

	filename := strings.TrimSpace(r.FormValue("filename"))
	if filename == "" && header != nil {
		filename = header.Filename
	}
	contentType := strings.TrimSpace(r.FormValue("contentType"))
	if contentType == "" && header != nil {
		contentType = header.Header.Get("Content-Type")
	}

	media, err := service.UploadRoomMedia(r.Context(), roomID, filename, contentType, file)
	if err != nil {
		writeCollabMediaError(w, err)
		return
	}
	OK(w, media)
}

// CollabRoomMediaList 列出房间共享媒体（任意成员可见）。
func CollabRoomMediaList(w http.ResponseWriter, r *http.Request, roomID string) {
	list, err := service.RoomMediaList(r.Context(), roomID)
	if err != nil {
		writeCollabMediaError(w, err)
		return
	}
	OK(w, list)
}

// CollabRoomMediaMeta 读取单个房间媒体元数据（任意成员可见）。
func CollabRoomMediaMeta(w http.ResponseWriter, r *http.Request, roomID, mediaID string) {
	media, err := service.RoomMediaMeta(r.Context(), roomID, mediaID)
	if err != nil {
		writeCollabMediaError(w, err)
		return
	}
	OK(w, media)
}

// CollabRoomMediaContent 播放房间媒体（任意成员可见，支持 HTTP Range 以便视频拖动）。
func CollabRoomMediaContent(w http.ResponseWriter, r *http.Request, roomID, mediaID string) {
	serveCollabRoomMedia(w, r, roomID, mediaID, false)
}

// CollabRoomMediaDownload 下载房间媒体（任意成员可见，带附件头）。
func CollabRoomMediaDownload(w http.ResponseWriter, r *http.Request, roomID, mediaID string) {
	serveCollabRoomMedia(w, r, roomID, mediaID, true)
}

// CollabDeleteRoomMedia 删除房间媒体（仅 owner）。
func CollabDeleteRoomMedia(w http.ResponseWriter, r *http.Request, roomID, mediaID string) {
	if err := service.DeleteRoomMedia(r.Context(), roomID, mediaID); err != nil {
		writeCollabMediaError(w, err)
		return
	}
	OK(w, nil)
}

func serveCollabRoomMedia(w http.ResponseWriter, r *http.Request, roomID, mediaID string, download bool) {
	media, file, err := service.RoomMediaContent(r.Context(), roomID, mediaID)
	if err != nil {
		writeCollabMediaError(w, err)
		return
	}
	defer file.Close()

	// 一律禁止浏览器对响应做 MIME 嗅探，避免把非内联安全内容当成 HTML/脚本执行。
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 房间媒体是私有的，禁止共享缓存。
	w.Header().Set("Cache-Control", "private, no-store")

	// Content-Type 只能使用上传时校验通过并存下来的类型，绝不回显客户端声明值；
	// 非内联安全类型（含历史遗留的 image/svg+xml、text/html 等主动内容）强制附件下载，
	// 并使用通用二进制类型，确保不会在同源被当作可执行内容内联渲染。
	inlineSafe := service.RoomMediaInlineSafe(media.MimeType)
	w.Header().Set("Content-Type", service.RoomMediaResponseContentType(media.MimeType))
	if download || !inlineSafe {
		w.Header().Set("Content-Disposition", contentDispositionAttachment(media.Filename))
	}
	// ServeContent 处理 Range/If-Range/If-Modified-Since 并正确返回 206 与 Content-Range。
	http.ServeContent(w, r, media.Filename, media.CreatedAt, file)
}

func contentDispositionAttachment(filename string) string {
	name := strings.TrimSpace(filename)
	if name == "" {
		name = "media"
	}
	name = strings.ReplaceAll(strings.ReplaceAll(name, "\"", ""), "\r", "")
	return "attachment; filename=\"" + name + "\"; filename*=UTF-8''" + url.PathEscape(name)
}

// writeCollabMediaError 把房间媒体业务错误映射为统一响应结构与 HTTP 状态码。
func writeCollabMediaError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrRoomNotFound), errors.Is(err, service.ErrMediaNotFound):
		FailWithStatus(w, http.StatusNotFound, err.Error())
	case errors.Is(err, service.ErrRoomForbidden):
		FailWithStatus(w, http.StatusForbidden, err.Error())
	case errors.Is(err, service.ErrMediaUnsupported):
		FailWithStatus(w, http.StatusUnsupportedMediaType, err.Error())
	case errors.Is(err, service.ErrMediaTooLarge):
		FailWithStatus(w, http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, service.ErrRoomInvalid), errors.Is(err, service.ErrMediaInvalid):
		FailWithStatus(w, http.StatusBadRequest, err.Error())
	default:
		FailError(w, err)
	}
}
