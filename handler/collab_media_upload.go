package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tigerowo/infinite-canvas/service"
)

// 房间媒体分片上传 HTTP 入口（协作 v1，C04-03 #26）。
// 单次请求体远小于 Cloudflare 100MB 上限；合并、摘要校验与落盘登记在 service 层完成。

type createRoomMediaUploadRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	TotalBytes  int64  `json:"totalBytes"`
	Sha256      string `json:"sha256"`
}

type completeRoomMediaUploadRequest struct {
	Sha256 string `json:"sha256"`
}

// CollabCreateRoomMediaUpload 创建分片上传会话（owner/editor）。
func CollabCreateRoomMediaUpload(w http.ResponseWriter, r *http.Request, roomID string) {
	var req createRoomMediaUploadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		FailWithStatus(w, http.StatusBadRequest, service.ErrMediaInvalid.Error())
		return
	}
	session, err := service.CreateRoomMediaUpload(r.Context(), roomID, req.Filename, req.ContentType, req.TotalBytes, req.Sha256)
	if err != nil {
		writeCollabMediaError(w, err)
		return
	}
	OK(w, session)
}

// CollabUploadRoomMediaChunk 上传单个分片（owner/editor，幂等且允许乱序）。
func CollabUploadRoomMediaChunk(w http.ResponseWriter, r *http.Request, roomID, sessionID, index string) {
	chunkIndex, err := strconv.Atoi(strings.TrimSpace(index))
	if err != nil || chunkIndex < 0 {
		FailWithStatus(w, http.StatusBadRequest, service.ErrMediaInvalid.Error())
		return
	}
	// 限制单分片请求体，避免超出代理上限的分片先落盘。
	r.Body = http.MaxBytesReader(w, r.Body, service.MaxRoomMediaChunkBytes+(1<<20))
	progress, err := service.UploadRoomMediaChunk(r.Context(), roomID, sessionID, chunkIndex, r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			FailWithStatus(w, http.StatusRequestEntityTooLarge, service.ErrMediaTooLarge.Error())
			return
		}
		writeCollabMediaError(w, err)
		return
	}
	OK(w, progress)
}

// CollabCompleteRoomMediaUpload 合并分片、校验摘要并登记媒体（幂等）。
func CollabCompleteRoomMediaUpload(w http.ResponseWriter, r *http.Request, roomID, sessionID string) {
	var req completeRoomMediaUploadRequest
	// 允许空 body：摘要可在创建会话时已提供。
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	media, err := service.CompleteRoomMediaUpload(r.Context(), roomID, sessionID, req.Sha256)
	if err != nil {
		writeCollabMediaError(w, err)
		return
	}
	OK(w, media)
}

// CollabRoomMediaUploadProgress 查询上传会话进度（任意成员可见）。
func CollabRoomMediaUploadProgress(w http.ResponseWriter, r *http.Request, roomID, sessionID string) {
	progress, err := service.RoomMediaUploadProgress(r.Context(), roomID, sessionID)
	if err != nil {
		writeCollabMediaError(w, err)
		return
	}
	OK(w, progress)
}

// CollabAbortRoomMediaUpload 取消上传会话并清理临时分片（仅会话创建者）。
func CollabAbortRoomMediaUpload(w http.ResponseWriter, r *http.Request, roomID, sessionID string) {
	if err := service.AbortRoomMediaUpload(r.Context(), roomID, sessionID); err != nil {
		writeCollabMediaError(w, err)
		return
	}
	OK(w, nil)
}
