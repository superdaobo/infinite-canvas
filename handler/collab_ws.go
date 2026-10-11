package handler

import (
	"net/http"

	"github.com/tigerowo/infinite-canvas/internal/collab/realtime"
	"github.com/tigerowo/infinite-canvas/service"
)

// CollabCreateWSTicket 用主账号 JWT 换取短时效单次 WS 握手 ticket。
// JWT 不进入 URL；客户端拿到 ticket 后在 WS 首帧 hello 中携带。
func CollabCreateWSTicket(w http.ResponseWriter, r *http.Request, roomID string) {
	result, err := service.IssueRoomWSTicket(r.Context(), roomID)
	if err != nil {
		writeCollabRoomError(w, err)
		return
	}
	OK(w, result)
}

// CollabWS 将连接升级为 WebSocket 并交由实时层处理。
// 该端点不挂 UserAuth：身份通过首帧 hello 的 ticket 绑定，避免把 JWT 放进 URL。
func CollabWS(w http.ResponseWriter, r *http.Request, roomID string) {
	realtime.Serve(newWSResponseWriter(w), r, roomID, service.RealtimeResolver())
}

// wsResponseWriter 是 gin.ResponseWriter 的适配层。
//
// gin 的 Hijack 在 WriteHeaderNow 之后会拒绝（errHijackAlreadyWritten），而
// coder/websocket 检测到 WriteHeaderNow 就会调用它。这里刻意不暴露 WriteHeaderNow，
// 并把 101 直接写到未包装的底层 writer，既保证握手响应立即上线，又让 Hijack 可用。
type wsResponseWriter struct {
	http.ResponseWriter
	http.Hijacker
	raw http.ResponseWriter
}

func (w wsResponseWriter) WriteHeader(statusCode int) {
	w.raw.WriteHeader(statusCode)
}

// newWSResponseWriter 用底层 net/http writer 承载 101，保留 gin 的 Hijack 能力。
func newWSResponseWriter(w http.ResponseWriter) http.ResponseWriter {
	raw := w
	if unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter }); ok {
		if underlying := unwrapper.Unwrap(); underlying != nil {
			raw = underlying
		}
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		// 理论上 gin.ResponseWriter 一定实现 Hijacker；兜底返回原 writer 由 Serve 报错。
		return w
	}
	return wsResponseWriter{ResponseWriter: w, Hijacker: hijacker, raw: raw}
}
