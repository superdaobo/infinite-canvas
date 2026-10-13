package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

// 房间媒体分片上传（协作 v1，C04-03 #26）业务错误。handler 依据这些哨兵错误映射 HTTP 状态码。
var (
	// ErrMediaUploadNotFound 表示上传会话不存在、已过期或不属于当前用户（不泄漏存在性）。
	ErrMediaUploadNotFound = errors.New("上传会话不存在")
	// ErrMediaUploadExpired 表示上传会话已过期，需要重新创建会话。
	ErrMediaUploadExpired = errors.New("上传会话已过期")
	// ErrMediaUploadConflict 表示会话正在被另一个完成请求合并，或状态冲突，可稍后重试。
	ErrMediaUploadConflict = errors.New("上传会话正在处理中")
	// ErrMediaUploadState 表示会话当前状态不允许该操作（如已完成后再上传分片）。
	ErrMediaUploadState = errors.New("上传会话状态不允许该操作")
	// ErrMediaUploadIncomplete 表示分片不完整（缺失、乱序缺号或大小不符），完成被拒。
	ErrMediaUploadIncomplete = errors.New("上传分片不完整")
	// ErrMediaDigestMismatch 表示合并后内容的 SHA-256 摘要与客户端声明不一致。
	ErrMediaDigestMismatch = errors.New("媒体摘要校验失败")
)

var (
	// RoomMediaChunkBytes 是服务端为分片上传会话指定的固定分片大小（末片可更小）。
	// 设为变量以便测试注入更小的分片。
	RoomMediaChunkBytes int64 = 8 << 20
	// MaxRoomMediaChunkBytes 是单个分片请求体的上限，须明显小于 Cloudflare 100MB 请求体上限。
	MaxRoomMediaChunkBytes int64 = 16 << 20
	// MaxRoomMediaChunkedBytes 是分片上传单个文件的总大小上限。分片上传正是为了突破单请求
	// 路径的 MaxRoomMediaBytes（Cloudflare 100MB 请求体上限），因此这里给更大的上限。
	// 设为变量以便测试注入更小的阈值。
	MaxRoomMediaChunkedBytes int64 = 2 << 30
	// RoomMediaUploadTTL 是上传会话的有效期，过期后由清理任务回收临时分片。
	RoomMediaUploadTTL = 24 * time.Hour
)

// CreateRoomMediaUpload 以 owner/editor 身份创建分片上传会话。
// 客户端需声明文件名、内容类型、总字节数与 SHA-256 摘要（摘要也可在完成时补交）。
func CreateRoomMediaUpload(ctx context.Context, roomID, filename, contentType string, totalBytes int64, digest string) (model.RoomMediaUploadView, error) {
	member, err := RequireRoomWriter(ctx, roomID)
	if err != nil {
		return model.RoomMediaUploadView{}, err
	}
	roomID = strings.TrimSpace(roomID)
	contentType = cleanMimeType(contentType)
	if totalBytes <= 0 {
		return model.RoomMediaUploadView{}, ErrMediaInvalid
	}
	if totalBytes > MaxRoomMediaChunkedBytes {
		return model.RoomMediaUploadView{}, ErrMediaTooLarge
	}
	if !declaredRoomMediaTypeAllowed(contentType) {
		return model.RoomMediaUploadView{}, ErrMediaUnsupported
	}
	normalizedDigest, err := normalizeMediaDigest(digest)
	if err != nil {
		return model.RoomMediaUploadView{}, err
	}
	chunkBytes := RoomMediaChunkBytes
	if chunkBytes <= 0 {
		return model.RoomMediaUploadView{}, ErrMediaInvalid
	}
	chunkCount := int((totalBytes + chunkBytes - 1) / chunkBytes)

	now := time.Now().UTC()
	session := model.RoomMediaUpload{
		ID: newID("mup"), RoomID: roomID, CreatedBy: member.UserID,
		Filename: normalizeRoomMediaFilename(filename), MimeType: contentType,
		TotalBytes: totalBytes, ChunkBytes: chunkBytes, ChunkCount: chunkCount,
		Digest: normalizedDigest, Status: model.RoomMediaUploadStatusPending,
		ExpiresAt: now.Add(RoomMediaUploadTTL), CreatedAt: now, UpdatedAt: now,
	}
	if err := os.MkdirAll(roomMediaUploadDir(session.ID), 0o755); err != nil {
		return model.RoomMediaUploadView{}, err
	}
	if _, err := repository.SaveRoomMediaUpload(session); err != nil {
		_ = os.RemoveAll(roomMediaUploadDir(session.ID))
		return model.RoomMediaUploadView{}, err
	}
	// 顺手回收过期会话（尽力而为，失败不影响本次创建）。
	_, _ = CleanupExpiredRoomMediaUploads(now)
	return roomMediaUploadView(session, 0, 0), nil
}

// UploadRoomMediaChunk 上传单个分片（owner/editor，且必须是会话创建者）。
// 分片按序号落盘并原子替换，重复分片幂等、乱序上传不影响最终合并顺序。
func UploadRoomMediaChunk(ctx context.Context, roomID, sessionID string, index int, src io.Reader) (model.RoomMediaUploadView, error) {
	member, err := RequireRoomWriter(ctx, roomID)
	if err != nil {
		return model.RoomMediaUploadView{}, err
	}
	session, err := loadWritableRoomMediaUpload(roomID, sessionID, member.UserID)
	if err != nil {
		return model.RoomMediaUploadView{}, err
	}
	if index < 0 || index >= session.ChunkCount {
		return model.RoomMediaUploadView{}, ErrMediaInvalid
	}
	if src == nil {
		return model.RoomMediaUploadView{}, ErrMediaInvalid
	}
	expected := roomMediaChunkLength(session, index)
	dir := roomMediaUploadDir(session.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return model.RoomMediaUploadView{}, err
	}
	tmp, err := os.CreateTemp(dir, "chunk-*")
	if err != nil {
		return model.RoomMediaUploadView{}, err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	written, err := io.Copy(tmp, io.LimitReader(src, expected+1))
	if err != nil {
		return model.RoomMediaUploadView{}, err
	}
	if written != expected {
		return model.RoomMediaUploadView{}, ErrMediaInvalid
	}
	if err := tmp.Sync(); err != nil {
		return model.RoomMediaUploadView{}, err
	}
	if err := tmp.Close(); err != nil {
		return model.RoomMediaUploadView{}, err
	}
	// 原子替换：重复分片覆盖同名文件，天然幂等；合并端只会看到完整分片。
	if err := os.Rename(tmpPath, filepath.Join(dir, strconv.Itoa(index))); err != nil {
		return model.RoomMediaUploadView{}, err
	}
	received, receivedBytes := roomMediaUploadProgress(dir, session.ChunkCount)
	return roomMediaUploadView(session, received, receivedBytes), nil
}

// CompleteRoomMediaUpload 按序合并分片、校验总大小与 SHA-256 摘要后落盘登记媒体。
// 重复完成请求幂等：会话已完成时直接返回既有媒体。
func CompleteRoomMediaUpload(ctx context.Context, roomID, sessionID, digest string) (model.RoomMediaView, error) {
	member, err := RequireRoomWriter(ctx, roomID)
	if err != nil {
		return model.RoomMediaView{}, err
	}
	session, err := loadOwnedRoomMediaUpload(roomID, sessionID, member.UserID)
	if err != nil {
		return model.RoomMediaView{}, err
	}
	switch session.Status {
	case model.RoomMediaUploadStatusCompleted:
		return completedRoomMediaView(session)
	case model.RoomMediaUploadStatusAborted:
		return model.RoomMediaView{}, ErrMediaUploadState
	case model.RoomMediaUploadStatusCompleting:
		return model.RoomMediaView{}, ErrMediaUploadConflict
	}
	if uploadExpired(session, time.Now().UTC()) {
		return model.RoomMediaView{}, ErrMediaUploadExpired
	}
	expected := session.Digest
	if override := strings.TrimSpace(digest); override != "" {
		normalized, err := normalizeMediaDigest(override)
		if err != nil {
			return model.RoomMediaView{}, err
		}
		expected = normalized
	}
	if expected == "" {
		// 没有摘要就无法做完整性校验，拒绝落盘。
		return model.RoomMediaView{}, ErrMediaInvalid
	}

	// 抢占完成权：并发完成请求只有一个进入合并，其余按已完成/处理中处理。
	claimed, err := repository.CompareAndSetRoomMediaUploadStatus(
		session.ID, model.RoomMediaUploadStatusPending, model.RoomMediaUploadStatusCompleting, "")
	if err != nil {
		return model.RoomMediaView{}, err
	}
	if !claimed {
		if latest, ok, findErr := repository.FindRoomMediaUpload(session.ID); findErr == nil && ok {
			if latest.Status == model.RoomMediaUploadStatusCompleted {
				return completedRoomMediaView(latest)
			}
		}
		return model.RoomMediaView{}, ErrMediaUploadConflict
	}
	// 合并/校验失败时回退到 pending，允许客户端补齐或修正分片后重试。
	revert := func() {
		_, _ = repository.CompareAndSetRoomMediaUploadStatus(
			session.ID, model.RoomMediaUploadStatusCompleting, model.RoomMediaUploadStatusPending, "")
	}

	view, err := mergeRoomMediaUpload(session, expected)
	if err != nil {
		revert()
		return model.RoomMediaView{}, err
	}
	if _, err := repository.CompareAndSetRoomMediaUploadStatus(
		session.ID, model.RoomMediaUploadStatusCompleting, model.RoomMediaUploadStatusCompleted, view.ID); err != nil {
		revert()
		return model.RoomMediaView{}, err
	}
	// 登记成功，回收分片临时目录。
	_ = os.RemoveAll(roomMediaUploadDir(session.ID))
	return view, nil
}

// RoomMediaUploadProgress 返回上传会话进度（任意房间成员可见）。
func RoomMediaUploadProgress(ctx context.Context, roomID, sessionID string) (model.RoomMediaUploadView, error) {
	if _, err := CurrentUserRoomMember(ctx, roomID); err != nil {
		return model.RoomMediaUploadView{}, err
	}
	session, ok, err := repository.FindRoomMediaUpload(sessionID)
	if err != nil {
		return model.RoomMediaUploadView{}, err
	}
	if !ok || session.RoomID != strings.TrimSpace(roomID) {
		return model.RoomMediaUploadView{}, ErrMediaUploadNotFound
	}
	received, receivedBytes := roomMediaUploadProgress(roomMediaUploadDir(session.ID), session.ChunkCount)
	return roomMediaUploadView(session, received, receivedBytes), nil
}

// AbortRoomMediaUpload 取消上传会话并清理临时分片（仅会话创建者）。
func AbortRoomMediaUpload(ctx context.Context, roomID, sessionID string) error {
	member, err := RequireRoomWriter(ctx, roomID)
	if err != nil {
		return err
	}
	session, ok, err := repository.FindRoomMediaUpload(sessionID)
	if err != nil {
		return err
	}
	if !ok || session.RoomID != strings.TrimSpace(roomID) || session.CreatedBy != member.UserID {
		return ErrMediaUploadNotFound
	}
	if session.Status == model.RoomMediaUploadStatusCompleted {
		return ErrMediaUploadState
	}
	if _, err := repository.DeleteRoomMediaUpload(session.ID); err != nil {
		return err
	}
	_ = os.RemoveAll(roomMediaUploadDir(session.ID))
	return nil
}

// CleanupExpiredRoomMediaUploads 回收过期上传会话及其临时分片，返回清理数量。
func CleanupExpiredRoomMediaUploads(now time.Time) (int, error) {
	expired, err := repository.ListExpiredRoomMediaUploads(now)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, session := range expired {
		if _, err := repository.DeleteRoomMediaUpload(session.ID); err != nil {
			continue
		}
		_ = os.RemoveAll(roomMediaUploadDir(session.ID))
		removed++
	}
	return removed, nil
}

// mergeRoomMediaUpload 按序合并全部分片，校验大小与摘要后复用落位逻辑登记媒体。
func mergeRoomMediaUpload(session model.RoomMediaUpload, expectedDigest string) (model.RoomMediaView, error) {
	dir := roomMediaUploadDir(session.ID)
	// 先确认全部分片到位且大小精确匹配；缺失/不符立即拒绝，绝不落盘登记。
	for i := 0; i < session.ChunkCount; i++ {
		info, err := os.Stat(filepath.Join(dir, strconv.Itoa(i)))
		if err != nil {
			return model.RoomMediaView{}, ErrMediaUploadIncomplete
		}
		if info.Size() != roomMediaChunkLength(session, i) {
			return model.RoomMediaView{}, ErrMediaUploadIncomplete
		}
	}

	tmpDir := filepath.Join(roomMediaDir(), "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return model.RoomMediaView{}, err
	}
	merged, err := os.CreateTemp(tmpDir, "merge-*")
	if err != nil {
		return model.RoomMediaView{}, err
	}
	mergedPath := merged.Name()
	defer func() {
		_ = merged.Close()
		_ = os.Remove(mergedPath)
	}()

	hasher := sha256.New()
	writer := io.MultiWriter(merged, hasher)
	var total int64
	for i := 0; i < session.ChunkCount; i++ {
		chunk, err := os.Open(filepath.Join(dir, strconv.Itoa(i)))
		if err != nil {
			return model.RoomMediaView{}, ErrMediaUploadIncomplete
		}
		n, copyErr := io.Copy(writer, io.LimitReader(chunk, MaxRoomMediaChunkBytes+1))
		closeErr := chunk.Close()
		if copyErr != nil {
			return model.RoomMediaView{}, copyErr
		}
		if closeErr != nil {
			return model.RoomMediaView{}, closeErr
		}
		total += n
		if total > MaxRoomMediaChunkedBytes {
			return model.RoomMediaView{}, ErrMediaTooLarge
		}
	}
	if total != session.TotalBytes {
		return model.RoomMediaView{}, ErrMediaUploadIncomplete
	}
	if err := merged.Sync(); err != nil {
		return model.RoomMediaView{}, err
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(digest, expectedDigest) {
		return model.RoomMediaView{}, ErrMediaDigestMismatch
	}
	return finalizeRoomMedia(session.RoomID, session.CreatedBy, session.Filename, session.MimeType, digest, total, merged)
}

func loadWritableRoomMediaUpload(roomID, sessionID, userID string) (model.RoomMediaUpload, error) {
	session, err := loadOwnedRoomMediaUpload(roomID, sessionID, userID)
	if err != nil {
		return model.RoomMediaUpload{}, err
	}
	switch session.Status {
	case model.RoomMediaUploadStatusPending:
	case model.RoomMediaUploadStatusCompleting:
		return model.RoomMediaUpload{}, ErrMediaUploadConflict
	default:
		return model.RoomMediaUpload{}, ErrMediaUploadState
	}
	if uploadExpired(session, time.Now().UTC()) {
		return model.RoomMediaUpload{}, ErrMediaUploadExpired
	}
	return session, nil
}

func loadOwnedRoomMediaUpload(roomID, sessionID, userID string) (model.RoomMediaUpload, error) {
	session, ok, err := repository.FindRoomMediaUpload(sessionID)
	if err != nil {
		return model.RoomMediaUpload{}, err
	}
	if !ok || session.RoomID != strings.TrimSpace(roomID) || session.CreatedBy != strings.TrimSpace(userID) {
		return model.RoomMediaUpload{}, ErrMediaUploadNotFound
	}
	return session, nil
}

func completedRoomMediaView(session model.RoomMediaUpload) (model.RoomMediaView, error) {
	if strings.TrimSpace(session.MediaID) == "" {
		return model.RoomMediaView{}, ErrMediaUploadState
	}
	media, ok, err := repository.FindRoomMedia(session.RoomID, session.MediaID)
	if err != nil {
		return model.RoomMediaView{}, err
	}
	if !ok {
		return model.RoomMediaView{}, ErrMediaNotFound
	}
	return roomMediaView(media), nil
}

// roomMediaChunkLength 返回指定序号的期望分片字节数：除末片外均为固定分片大小，
// 末片为总大小减去前面分片后的剩余字节（至少 1）。
func roomMediaChunkLength(session model.RoomMediaUpload, index int) int64 {
	if index < session.ChunkCount-1 {
		return session.ChunkBytes
	}
	return session.TotalBytes - session.ChunkBytes*int64(session.ChunkCount-1)
}

func roomMediaUploadProgress(dir string, chunkCount int) (int, int64) {
	received, receivedBytes := 0, int64(0)
	for i := 0; i < chunkCount; i++ {
		if info, err := os.Stat(filepath.Join(dir, strconv.Itoa(i))); err == nil {
			received++
			receivedBytes += info.Size()
		}
	}
	return received, receivedBytes
}

func roomMediaUploadView(session model.RoomMediaUpload, received int, receivedBytes int64) model.RoomMediaUploadView {
	return model.RoomMediaUploadView{
		ID: session.ID, RoomID: session.RoomID, Filename: session.Filename, MimeType: session.MimeType,
		TotalBytes: session.TotalBytes, ChunkBytes: session.ChunkBytes, ChunkCount: session.ChunkCount,
		ReceivedChunks: received, ReceivedBytes: receivedBytes, Status: session.Status,
		MediaID: session.MediaID, ExpiresAt: session.ExpiresAt, CreatedAt: session.CreatedAt,
	}
}

func roomMediaUploadDir(sessionID string) string {
	return filepath.Join(roomMediaDir(), "tmp", "uploads", filepath.Base(strings.TrimSpace(sessionID)))
}

func uploadExpired(session model.RoomMediaUpload, now time.Time) bool {
	return !session.ExpiresAt.IsZero() && now.After(session.ExpiresAt)
}

// normalizeMediaDigest 规范化 SHA-256 摘要：空值原样返回，非空必须是 64 位十六进制。
func normalizeMediaDigest(digest string) (string, error) {
	digest = strings.ToLower(strings.TrimSpace(digest))
	if digest == "" {
		return "", nil
	}
	if len(digest) != 64 {
		return "", ErrMediaInvalid
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", ErrMediaInvalid
	}
	return digest, nil
}
