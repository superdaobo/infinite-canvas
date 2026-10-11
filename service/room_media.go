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
	roomID = strings.TrimSpace(roomID)
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
	size, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(src, MaxRoomMediaBytes+1))
	if err != nil {
		return model.RoomMediaView{}, err
	}
	if size == 0 {
		return model.RoomMediaView{}, ErrMediaInvalid
	}
	if size > MaxRoomMediaBytes {
		return model.RoomMediaView{}, ErrMediaTooLarge
	}
	if err := tmp.Sync(); err != nil {
		return model.RoomMediaView{}, err
	}
	digest := hex.EncodeToString(hasher.Sum(nil))

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

	relPath := filepath.ToSlash(filepath.Join(digest[:2], digest))
	absPath := filepath.Join(dir, filepath.FromSlash(relPath))
	if _, statErr := os.Stat(absPath); statErr != nil {
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return model.RoomMediaView{}, err
		}
		if err := os.Rename(tmpPath, absPath); err != nil {
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
		Bytes: size, StoragePath: relPath, CreatedBy: member.UserID, CreatedAt: now, UpdatedAt: now,
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

// classifyRoomMedia 依据声明的 Content-Type 与内容嗅探结果判定媒体类别。
// 只要声明类型或嗅探类型之一属于图片/视频/音频即通过，否则视为不支持。
func classifyRoomMedia(declared string, header []byte) (model.RoomMediaKind, string, bool) {
	candidates := make([]string, 0, 2)
	if ct := cleanMimeType(declared); ct != "" {
		candidates = append(candidates, ct)
	}
	candidates = append(candidates, cleanMimeType(http.DetectContentType(header)))
	for _, ct := range candidates {
		if kind, ok := roomMediaKindForMime(ct); ok {
			return kind, ct, true
		}
	}
	return "", "", false
}

func roomMediaKindForMime(mimeType string) (model.RoomMediaKind, bool) {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return model.RoomMediaKindImage, true
	case strings.HasPrefix(mimeType, "video/"):
		return model.RoomMediaKindVideo, true
	case strings.HasPrefix(mimeType, "audio/"):
		return model.RoomMediaKindAudio, true
	default:
		return "", false
	}
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
