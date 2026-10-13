package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tigerowo/infinite-canvas/internal/collab/contract"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/service"
)

// maxCollabImportBodyBytes 限制单次旧画布导入请求体的最大字节数（8 MiB）。
// 依据：旧画布是整份 JSON（节点 + 连线 + 公共 metadata），77 节点样本含提示词等文本；
// 8 MiB 足以覆盖正常整份快照，同时在鉴权/成员校验之前阻止超大 JSON 消耗内存。
const maxCollabImportBodyBytes = 8 << 20

// CollabImportRoom 导入旧单人画布到共享房间（C04-02 / #25）。
// mode 为空或 preview 时只读预扫描；显式 mode=initial 才写入空房间。
// 响应为 {code,data,msg}；契约错误码放在 data.error，HTTP 状态由 StatusFor 决定。
func CollabImportRoom(w http.ResponseWriter, r *http.Request, roomID string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCollabImportBodyBytes)
	var request model.RoomImportRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeCollabOpError(w, &contract.Error{Code: contract.CodePayloadTooLarge, Message: "导入请求体超过大小上限"})
			return
		}
		writeCollabOpError(w, &contract.Error{Code: contract.CodeInvalidPayload, Message: "导入请求体不是合法的 JSON"})
		return
	}
	report, err := service.ImportRoomSnapshot(r.Context(), roomID, request)
	if err != nil {
		writeCollabOpError(w, err)
		return
	}
	OK(w, report)
}
