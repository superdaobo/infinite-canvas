package service

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/tigerowo/infinite-canvas/model"
)

// MockSchoolUpstreamClient 是 SchoolUpstreamClient 的最小 mock 实现（C03-02 / #18）。
//
// 用假数据模拟学校上游的提交/查询/取结果，供本 issue 的队列与 #22 的结果回填复用。
// 绝不接触真实学校设备、Cookie、Chrome Profile 或 Comfy Bridge；返回的产物 URL
// 是 `example.invalid` 占位地址，真实回填时必须先经 SSRF 白名单校验（安全边界 §5.4）。
type MockSchoolUpstreamClient struct {
	mu    sync.Mutex
	seq   int
	tasks map[string]SchoolUpstreamTask
}

// NewMockSchoolUpstreamClient 创建一个空的 mock 上游客户端。
func NewMockSchoolUpstreamClient() *MockSchoolUpstreamClient {
	return &MockSchoolUpstreamClient{tasks: map[string]SchoolUpstreamTask{}}
}

// Submit 生成确定性的假上游任务号并记录初始状态。
func (c *MockSchoolUpstreamClient) Submit(_ context.Context, task model.SchoolTask) (SchoolUpstreamTask, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	id := fmt.Sprintf("mock-upstream-%d", c.seq)
	state := SchoolUpstreamTask{
		UpstreamTaskID: id,
		Status:         model.SchoolTaskRunning,
		Progress:       0,
	}
	c.tasks[id] = state
	return state, nil
}

// Query 返回假上游任务的当前状态：mock 任务直接视为成功并给出占位产物。
func (c *MockSchoolUpstreamClient) Query(_ context.Context, upstreamTaskID string) (SchoolUpstreamTask, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.tasks[strings.TrimSpace(upstreamTaskID)]
	if !ok {
		return SchoolUpstreamTask{}, fmt.Errorf("mock 上游任务不存在: %s", upstreamTaskID)
	}
	state.Status = model.SchoolTaskSucceeded
	state.Progress = 100
	state.ResultURLs = mockSchoolResultURLs(state.UpstreamTaskID)
	return state, nil
}

// Result 返回假上游任务的占位产物 URL 列表。
func (c *MockSchoolUpstreamClient) Result(_ context.Context, upstreamTaskID string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.tasks[strings.TrimSpace(upstreamTaskID)]; !ok {
		return nil, fmt.Errorf("mock 上游任务不存在: %s", upstreamTaskID)
	}
	return mockSchoolResultURLs(strings.TrimSpace(upstreamTaskID)), nil
}

func mockSchoolResultURLs(upstreamTaskID string) []string {
	return []string{fmt.Sprintf("https://example.invalid/school-mock/%s/result-0.png", upstreamTaskID)}
}
