package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 房间媒体业务错误。handler 依据这些哨兵错误映射 HTTP 状态码。
var (
	// ErrMediaNotFound 表示媒体不存在、已被删除，或当前用户无权访问（不泄漏存在性）。
	ErrMediaNotFound = errors.New("媒体不存在")
	// ErrMediaUnsupported 表示媒体类型不在允许范围（仅图片/视频/音频）。
	ErrMediaUnsupported = errors.New("不支持的媒体类型")
	// ErrMediaTooLarge 表示媒体文件超过服务端大小限制。
	ErrMediaTooLarge = errors.New("媒体文件超过大小限制")
	// ErrMediaInvalid 表示媒体参数无效（空文件等）。
	ErrMediaInvalid = errors.New("媒体参数无效")
)

const (
	// defaultMediaDir 是房间媒体私有内容寻址目录的默认位置。
	defaultMediaDir = "data/collab-media"
	// maxRoomMediaFilenameRunes 限制原始文件名长度，避免超长写入。
	maxRoomMediaFilenameRunes = 200
)

// MaxRoomMediaBytes 是单个房间媒体文件的服务端大小上限。
// 设为变量以便测试注入更小的阈值；Cloudflare 100MB 请求上限由 #26 单独处理。
var MaxRoomMediaBytes int64 = 64 << 20

// roomMediaDir 返回房间媒体私有存储根目录。
func roomMediaDir() string {
	dir := strings.TrimSpace(config.Cfg.MediaDir)
	if dir == "" {
		dir = defaultMediaDir
	}
	return dir
}

// roomMediaBlobPath 由私有相对路径拼出绝对路径，并防止路径穿越。
func roomMediaBlobPath(storagePath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(storagePath))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", ErrMediaNotFound
	}
	return filepath.Join(roomMediaDir(), clean), nil
}

// UploadRoomMedia 以 owner/editor 身份上传房间共享媒体。
// 内容按 SHA-256 摘要命名并去重；同一房间重复上传相同内容返回既有记录。
func UploadRoomMedia(ctx context.Context, roomID, filename, contentType string, src io.Reader) (model.RoomMediaView, error) {
	member, err := RequireRoomWriter(ctx, roomID)
	if err != nil {
		return model.RoomMediaView{}, err
	}
	return storeRoomMedia(roomID, member.UserID, filename, contentType, src)
}

// storeRoomMedia 是房间媒体的落地实现：内容按 SHA-256 摘要命名并去重，归属人由
// 调用方给定。受信任的服务端路径（学校结果回填 #22）以任务提交人作为归属人调用，
// 不经过用户 context；归属人必须是该房间的可写成员（由调用方先行校验）。
func storeRoomMedia(roomID, actorID, filename, contentType string, src io.Reader) (model.RoomMediaView, error) {
	return storeRoomMediaWithLimit(roomID, actorID, filename, contentType, src, MaxRoomMediaBytes)
}

// storeRoomMediaWithLimit 把字节流写入临时文件并做摘要与大小校验，再落位登记。
// 单请求路径使用 MaxRoomMediaBytes；分片上传合并路径使用更大的 MaxRoomMediaChunkedBytes。
func storeRoomMediaWithLimit(roomID, actorID, filename, contentType string, src io.Reader, maxBytes int64) (model.RoomMediaView, error) {
	roomID = strings.TrimSpace(roomID)
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return model.RoomMediaView{}, ErrMediaInvalid
	}
	if src == nil {
		return model.RoomMediaView{}, ErrMediaInvalid
	}
	filename = normalizeRoomMediaFilename(filename)

	dir := roomMediaDir()
	tmpDir := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return model.RoomMediaView{}, err
	}
	tmp, err := os.CreateTemp(tmpDir, "upload-*")
	if err != nil {
		return model.RoomMediaView{}, err
	}
	tmpPath := tmp.Name()
	// 无论成功与否都清理临时文件。
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(src, maxBytes+1))
	if err != nil {
		return model.RoomMediaView{}, err
	}
	if size == 0 {
		return model.RoomMediaView{}, ErrMediaInvalid
	}
	if size > maxBytes {
		return model.RoomMediaView{}, ErrMediaTooLarge
	}
	if err := tmp.Sync(); err != nil {
		return model.RoomMediaView{}, err
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	return finalizeRoomMedia(roomID, actorID, filename, contentType, digest, size, tmp)
}

// finalizeRoomMedia 把已完整写入的临时文件按内容摘要落位并登记为房间媒体，供单请求
// 路径与分片合并路径复用。调用方持有 tmp 的所有权：本函数会关闭 tmp；成功时临时文件
// 已 rename 到内容寻址路径，失败时由调用方负责删除临时文件。
func finalizeRoomMedia(roomID, actorID, filename, contentType, digest string, size int64, tmp *os.File) (model.RoomMediaView, error) {
	header := make([]byte, 512)
	read, _ := tmp.ReadAt(header, 0)
	kind, mimeType, ok := classifyRoomMedia(contentType, header[:read])
	if !ok {
		return model.RoomMediaView{}, ErrMediaUnsupported
	}
	// Windows 不允许重命名仍被占用的文件，先关闭临时文件再落位。
	if err := tmp.Close(); err != nil {
		return model.RoomMediaView{}, err
	}

	dir := roomMediaDir()
	relPath := filepath.ToSlash(filepath.Join(digest[:2], digest))
	absPath := filepath.Join(dir, filepath.FromSlash(relPath))
	if _, statErr := os.Stat(absPath); statErr != nil {
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return model.RoomMediaView{}, err
		}
		if err := os.Rename(tmp.Name(), absPath); err != nil {
			return model.RoomMediaView{}, err
		}
	}

	// 行级去重：同一房间相同摘要只保留一条记录，重复上传不重复占位。
	if existing, found, err := repository.FindRoomMedia(roomID, digest); err != nil {
		return model.RoomMediaView{}, err
	} else if found {
		return roomMediaView(existing), nil
	}

	now := time.Now().UTC()
	media := model.RoomMedia{
		RoomID: roomID, ID: digest, Kind: kind, Filename: filename, MimeType: mimeType,
		Bytes: size, StoragePath: relPath, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := repository.SaveRoomMedia(media); err != nil {
		return model.RoomMediaView{}, err
	}
	return roomMediaView(media), nil
}

// RoomMediaMeta 返回房间媒体元数据（任意成员可见）。
func RoomMediaMeta(ctx context.Context, roomID, mediaID string) (model.RoomMediaView, error) {
	if _, err := CurrentUserRoomMember(ctx, roomID); err != nil {
		return model.RoomMediaView{}, err
	}
	media, ok, err := repository.FindRoomMedia(roomID, mediaID)
	if err != nil {
		return model.RoomMediaView{}, err
	}
	if !ok {
		return model.RoomMediaView{}, ErrMediaNotFound
	}
	return roomMediaView(media), nil
}

// RoomMediaList 返回房间媒体列表（任意成员可见）。
func RoomMediaList(ctx context.Context, roomID string) (model.RoomMediaList, error) {
	if _, err := CurrentUserRoomMember(ctx, roomID); err != nil {
		return model.RoomMediaList{}, err
	}
	rows, err := repository.ListRoomMedia(roomID)
	if err != nil {
		return model.RoomMediaList{}, err
	}
	items := make([]model.RoomMediaView, 0, len(rows))
	for _, row := range rows {
		items = append(items, roomMediaView(row))
	}
	return model.RoomMediaList{Items: items, Total: int64(len(items))}, nil
}

// RoomMediaContent 打开房间媒体字节流用于播放/下载（任意成员可见）。
// 调用方负责关闭返回的文件。
func RoomMediaContent(ctx context.Context, roomID, mediaID string) (model.RoomMedia, *os.File, error) {
	if _, err := CurrentUserRoomMember(ctx, roomID); err != nil {
		return model.RoomMedia{}, nil, err
	}
	media, ok, err := repository.FindRoomMedia(roomID, mediaID)
	if err != nil {
		return model.RoomMedia{}, nil, err
	}
	if !ok {
		return model.RoomMedia{}, nil, ErrMediaNotFound
	}
	file, err := openRoomMediaBlob(media.StoragePath)
	if err != nil {
		return model.RoomMedia{}, nil, ErrMediaNotFound
	}
	return media, file, nil
}

// DeleteRoomMedia 删除房间媒体（仅 owner）。删除索引后，若已无任何房间引用同一内容，回收私有 blob。
func DeleteRoomMedia(ctx context.Context, roomID, mediaID string) error {
	if _, err := RequireRoomOwner(ctx, roomID); err != nil {
		return err
	}
	media, ok, err := repository.FindRoomMedia(roomID, mediaID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrMediaNotFound
	}
	if _, err := repository.DeleteRoomMedia(roomID, mediaID); err != nil {
		return err
	}
	if remaining, countErr := repository.CountRoomMediaByStoragePath(media.StoragePath); countErr == nil && remaining == 0 {
		if path, pathErr := roomMediaBlobPath(media.StoragePath); pathErr == nil {
			_ = os.Remove(path)
		}
	}
	return nil
}

// AuthorizeFileRead 是公开文件读取路径（GET /api/files/:id[/content]）的授权收敛点（R1）。
// 房间级共享媒体绝不通过公开对象 ID 路径暴露：命中房间媒体 ID 时一律按不存在处理，
// 房间媒体必须走 /api/v1/collab/rooms/:id/media 的成员鉴权接口。
// 个人/匿名/公开对象不在 collab_room_media 表内，行为完全不变，旧单人模式不受影响。
func AuthorizeFileRead(id string) error {
	isMedia, err := repository.IsRoomMediaID(id)
	if err != nil {
		return err
	}
	if isMedia {
		return ErrMediaNotFound
	}
	return nil
}

func openRoomMediaBlob(storagePath string) (*os.File, error) {
	path, err := roomMediaBlobPath(storagePath)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func roomMediaView(media model.RoomMedia) model.RoomMediaView {
	return model.RoomMediaView{
		RoomID: media.RoomID, ID: media.ID, StorageKey: "room:" + media.RoomID + ":" + media.ID,
		Kind: media.Kind, Filename: media.Filename, MimeType: media.MimeType, Bytes: media.Bytes,
		CreatedBy: media.CreatedBy, CreatedAt: media.CreatedAt,
		PlayURL:     roomMediaURL(media.RoomID, media.ID, "content"),
		DownloadURL: roomMediaURL(media.RoomID, media.ID, "download"),
	}
}

func roomMediaURL(roomID, mediaID, action string) string {
	return "/api/v1/collab/rooms/" + url.PathEscape(roomID) + "/media/" + url.PathEscape(mediaID) + "/" + action
}

func normalizeRoomMediaFilename(filename string) string {
	name := strings.TrimSpace(filepath.Base(strings.ReplaceAll(filename, "\\", "/")))
	if name == "" || name == "." || name == "/" {
		return "media"
	}
	if utf8.RuneCountInString(name) > maxRoomMediaFilenameRunes {
		name = string([]rune(name)[:maxRoomMediaFilenameRunes])
	}
	return name
}

// safeRoomMediaMimeKinds 是房间共享媒体唯一允许的显式安全类型白名单（精确匹配）。
// 只放可被浏览器当作惰性二进制/媒体资源渲染、不含脚本执行能力的类型；
// image/svg+xml、text/html、text/xml、application/xhtml+xml 等主动内容一律不在内。
var safeRoomMediaMimeKinds = map[string]model.RoomMediaKind{
	"image/png":  model.RoomMediaKindImage,
	"image/jpeg": model.RoomMediaKindImage,
	"image/webp": model.RoomMediaKindImage,
	"image/gif":  model.RoomMediaKindImage,

	"video/mp4":  model.RoomMediaKindVideo,
	"video/webm": model.RoomMediaKindVideo,

	"audio/mpeg":  model.RoomMediaKindAudio,
	"audio/wav":   model.RoomMediaKindAudio,
	"audio/x-wav": model.RoomMediaKindAudio,
	"audio/wave":  model.RoomMediaKindAudio,
	"audio/ogg":   model.RoomMediaKindAudio,
	"audio/mp4":   model.RoomMediaKindAudio,
	"audio/webm":  model.RoomMediaKindAudio,
}

// activeContentMimeTypes 是绝不允许作为房间媒体落库的主动内容类型。
// 这些类型在同源内联返回时可在成员浏览器中执行脚本，是本次加固的核心拒绝面。
var activeContentMimeTypes = map[string]bool{
	"image/svg+xml":            true,
	"text/html":                true,
	"text/xml":                 true,
	"application/xml":          true,
	"application/xhtml+xml":    true,
	"application/javascript":   true,
	"text/javascript":          true,
	"application/ecmascript":   true,
	"text/ecmascript":          true,
	"application/x-javascript": true,
	"application/xhtml":        true,
}

// RoomMediaInlineSafe 判断已存储的媒体类型是否可以安全地同源内联渲染。
// 非白名单类型（含历史遗留的 image/svg+xml、text/html 等主动内容）一律返回 false，
// 调用方应强制附件下载并使用通用二进制类型。
func RoomMediaInlineSafe(mimeType string) bool {
	_, ok := safeRoomMediaMimeKinds[cleanMimeType(mimeType)]
	return ok
}

// RoomMediaResponseContentType 返回响应应使用的 Content-Type：只接受上传时校验通过的
// 内联安全白名单类型，其余（历史遗留主动内容或未知类型）一律回退为通用二进制类型。
// 绝不回显客户端在上传时声明的原始值。
func RoomMediaResponseContentType(mimeType string) string {
	if ct := cleanMimeType(mimeType); RoomMediaInlineSafe(ct) {
		return ct
	}
	return "application/octet-stream"
}

// classifyRoomMedia 依据声明类型与内容嗅探结果判定媒体类别，返回落库使用的类型。
//
// 判定策略（拒绝客户端自报类型绕过）：
//  1. 声明类型与嗅探类型只要命中主动内容（SVG/HTML/XML/JS）直接拒绝；
//  2. 声明类型必须精确落在安全白名单内，任意 image/* 不再放行；
//  3. 图片必须由内容嗅探确认为白名单图片类型，杜绝把 SVG/HTML 文本伪装成图片；
//     视频/音频容器 Go 无法可靠嗅探，仅要求声明在白名单且内容非主动内容。
//
// 返回的 mimeType 始终是白名单内的类型，即上传时校验通过并存下来的类型。
func classifyRoomMedia(declared string, header []byte) (model.RoomMediaKind, string, bool) {
	declared = cleanMimeType(declared)
	sniffed := cleanMimeType(http.DetectContentType(header))
	if activeContentMimeTypes[declared] || activeContentMimeTypes[sniffed] {
		return "", "", false
	}
	kind, ok := safeRoomMediaMimeKinds[declared]
	if !ok {
		return "", "", false
	}
	if kind == model.RoomMediaKindImage {
		sniffKind, sniffOK := safeRoomMediaMimeKinds[sniffed]
		if !sniffOK || sniffKind != model.RoomMediaKindImage {
			return "", "", false
		}
	}
	return kind, declared, true
}

// declaredRoomMediaTypeAllowed 只依据声明类型做前置白名单校验（不嗅探内容），
// 用于分片上传会话创建时快速拒绝明显非法/主动内容的类型；内容真伪在完成时再由
// classifyRoomMedia 用实际字节复核。
func declaredRoomMediaTypeAllowed(declared string) bool {
	declared = cleanMimeType(declared)
	if declared == "" || activeContentMimeTypes[declared] {
		return false
	}
	_, ok := safeRoomMediaMimeKinds[declared]
	return ok
}

func cleanMimeType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, _, err := mime.ParseMediaType(value); err == nil {
		value = parsed
	}
	return strings.ToLower(value)
}
