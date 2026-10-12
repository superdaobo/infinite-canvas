package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
)

// 设备侧最小执行器（C03-03 / #19）：复用受限设备身份，走受限任务 API 完成一次
// 完整设备循环：领取 → 提交上游（mock）→ 绑定上游任务号 → 轮询 → 回传结果 → 结束。
//
// 本实现只使用设备令牌与设备受限接口，不接触用户 JWT、房间列表或模型密钥；
// 上游一律走 mock（SchoolUpstreamClient），绝不接触真实学校设备/Cookie/Chrome/Comfy Bridge。
// 可被本地脚本或测试直接驱动（见 router 集成测试）。
type SchoolExecutorClient struct {
	// BaseURL 是服务端根地址，例如 http://127.0.0.1:8080。
	BaseURL string
	// Token 是设备令牌明文（Bearer）；绝不落盘、不进日志。
	Token string
	// AccountRef 是设备侧学校账号的不可逆指纹，随领取/心跳上报。
	AccountRef string
	// HTTPClient 为空时使用带超时的默认客户端。
	HTTPClient *http.Client
	// Upstream 是模拟学校上游的客户端；为空时使用 mock 实现。
	Upstream SchoolUpstreamClient
}

// NewSchoolExecutorClient 构造一个设备侧执行器。
func NewSchoolExecutorClient(baseURL, token, accountRef string, upstream SchoolUpstreamClient) *SchoolExecutorClient {
	return &SchoolExecutorClient{
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:      strings.TrimSpace(token),
		AccountRef: strings.TrimSpace(accountRef),
		Upstream:   upstream,
	}
}

// SchoolExecutorRunResult 描述一次设备循环的结果。
type SchoolExecutorRunResult struct {
	Claimed        bool   `json:"claimed"`
	Blocked        bool   `json:"blocked"`
	TaskID         string `json:"taskId,omitempty"`
	Status         string `json:"status,omitempty"`
	UpstreamTaskID string `json:"upstreamTaskId,omitempty"`
	Backfilled     bool   `json:"backfilled,omitempty"`
}

// executorEnvelope 是统一响应结构 {code,data,msg} 的设备侧解析视图。
type executorEnvelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
	Msg  string          `json:"msg"`
}

// RunOnce 执行一次完整设备循环；无任务或单执行槽被占用时直接返回，不阻塞。
func (c *SchoolExecutorClient) RunOnce(ctx context.Context) (SchoolExecutorRunResult, error) {
	var claim model.SchoolTaskClaim
	if err := c.call(ctx, http.MethodPost, "/api/bridge/school/claim", map[string]any{}, &claim); err != nil {
		return SchoolExecutorRunResult{}, err
	}
	if claim.Task == nil {
		return SchoolExecutorRunResult{Blocked: claim.Blocked}, nil
	}
	task := *claim.Task
	result := SchoolExecutorRunResult{Claimed: true, TaskID: task.ID}

	submitted, err := c.upstream().Submit(ctx, model.SchoolTask{
		ID: task.ID, RoomID: task.RoomID, Channel: task.Channel, Params: task.Params,
	})
	if err != nil {
		return result, fmt.Errorf("提交 mock 学校上游失败: %w", err)
	}
	result.UpstreamTaskID = submitted.UpstreamTaskID
	if err := c.call(ctx, http.MethodPost, "/api/bridge/school/update", map[string]any{
		"taskId": task.ID, "upstreamTaskId": submitted.UpstreamTaskID,
	}, nil); err != nil {
		return result, err
	}

	state, err := c.upstream().Query(ctx, submitted.UpstreamTaskID)
	if err != nil {
		return result, fmt.Errorf("查询 mock 学校上游失败: %w", err)
	}
	switch state.Status {
	case model.SchoolTaskSucceeded:
		urls := state.ResultURLs
		if len(urls) == 0 {
			if urls, err = c.upstream().Result(ctx, submitted.UpstreamTaskID); err != nil {
				return result, fmt.Errorf("取 mock 学校上游结果失败: %w", err)
			}
		}
		if err := c.call(ctx, http.MethodPost, "/api/bridge/school/result", map[string]any{
			"taskId": task.ID, "upstreamTaskId": submitted.UpstreamTaskID, "resultUrls": urls,
		}, nil); err != nil {
			return result, err
		}
		result.Status = string(model.SchoolTaskSucceeded)
		result.Backfilled = true
	case model.SchoolTaskFailed:
		if err := c.call(ctx, http.MethodPost, "/api/bridge/school/finish", map[string]any{
			"taskId": task.ID, "status": string(model.SchoolTaskFailed), "error": state.Error,
		}, nil); err != nil {
			return result, err
		}
		result.Status = string(model.SchoolTaskFailed)
	default:
		// 上游仍在执行：返回当前状态，由调用方稍后重试轮询。
		result.Status = string(state.Status)
	}
	return result, nil
}

// Heartbeat 上报设备心跳与当前学校账号指纹，返回服务端的账号冲突判定。
func (c *SchoolExecutorClient) Heartbeat(ctx context.Context) (SchoolExecutorHeartbeatResult, error) {
	var heartbeat SchoolExecutorHeartbeatResult
	err := c.call(ctx, http.MethodPost, "/api/bridge/school/heartbeat",
		map[string]any{"schoolAccountRef": c.AccountRef}, &heartbeat)
	return heartbeat, err
}

func (c *SchoolExecutorClient) upstream() SchoolUpstreamClient {
	if c.Upstream != nil {
		return c.Upstream
	}
	return NewMockSchoolUpstreamClient()
}

func (c *SchoolExecutorClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// call 以设备令牌调用受限任务 API，解析 {code,data,msg} 并把契约错误码还原为错误。
func (c *SchoolExecutorClient) call(ctx context.Context, method, path string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var envelope executorEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("设备接口 %s 返回非 JSON: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || envelope.Code != 0 {
		return fmt.Errorf("设备接口 %s 失败: HTTP %d code=%d msg=%s", path, resp.StatusCode, envelope.Code, envelope.Msg)
	}
	if out == nil || len(envelope.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("设备接口 %s 响应解析失败: %w", path, err)
	}
	return nil
}
