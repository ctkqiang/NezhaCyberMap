package ingest

/*
本文件测试有界 Worker Pool 的并发上限与 context 取消行为。
*/

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/internal/feed"
)

// TestPoolMaxConcurrency 验证并发任务数不超过信号量上限。
func TestPoolMaxConcurrency(t *testing.T) {
	p := NewPool(3)
	ctx := context.Background()

	var running, peak atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(6)
	for i := 0; i < 6; i++ {
		p.Go(ctx, func(ctx context.Context) {
			defer wg.Done()
			cur := running.Add(1)
			for {
				pk := peak.Load()
				if cur > pk && peak.CompareAndSwap(pk, cur) {
					break
				}
				if cur <= pk {
					break
				}
			}
			<-release
			running.Add(-1)
		})
	}
	time.Sleep(50 * time.Millisecond)
	if got := peak.Load(); got > 3 {
		t.Errorf("峰值并发 = %d，期望 <= 3", got)
	}
	close(release)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("任务未在预期时间内完成")
	}
}

// TestPoolCanceledContext 验证 ctx 已取消时不启动任务。
func TestPoolCanceledContext(t *testing.T) {
	p := NewPool(2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预先取消

	started := false
	p.Go(ctx, func(ctx context.Context) {
		started = true
	})
	time.Sleep(20 * time.Millisecond)
	if started {
		t.Error("ctx 已取消时任务不应启动")
	}
}

// TestPipelineMockOnly 验证 mock 管道的事件产出与 ctx 取消清理。
func TestPipelineMockOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := New(true)
	events, _ := p.Start(ctx)

	// 700ms 间隔的 mock feed：2 秒内应收到至少 1 条事件。
	var got int
	timeout := time.After(2 * time.Second)
loop:
	for {
		select {
		case <-events:
			got++
			if got >= 2 {
				break loop
			}
		case <-timeout:
			break loop
		}
	}
	if got == 0 {
		t.Fatal("mock 管道 2 秒内未产出任何事件")
	}
	cancel()
	// 等待 goroutine 排空（背压解除 + ctx 退出）。
	time.Sleep(100 * time.Millisecond)
}

// authFeed 是返回认证错误的测试 feed。
type authFeed struct{}

// Name 返回测试 feed 名。
func (authFeed) Name() string { return "AuthTest" }

// Run 始终返回认证错误。
func (authFeed) Run(context.Context, chan<- feed.AttackEvent) error {
	return fmt.Errorf("%w: HTTP 401", feed.ErrAuthRequired)
}

// TestPipelineAuthDegrades 验证认证失败立即降级且不重试。
func TestPipelineAuthDegrades(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(false, Spec(authFeed{}, 10*time.Millisecond))
	_, statuses := p.Start(ctx)

	// 认证失败 → 立即降级（状态消息带「需 API 密钥」说明）。
	var st FeedStatus
	deadline := time.After(2 * time.Second)
loop2:
	for {
		select {
		case st = <-statuses:
			if st.Name == "AuthTest" && st.State == FeedDegraded {
				break loop2
			}
		case <-deadline:
			t.Fatal("2 秒内未收到降级状态消息")
		}
	}
	if st.Name != "AuthTest" || st.State != FeedDegraded {
		t.Fatalf("状态 = %s/%v，期望 AuthTest/降级", st.Name, st.State)
	}
	if !strings.Contains(st.Message, "abuse-key") {
		t.Errorf("降级说明缺少密钥指引: %q", st.Message)
	}
	// 本会话不再重试：等待后状态计数不再变化（runFeed 已退出）。
	time.Sleep(100 * time.Millisecond)
	p.mu.Lock()
	state := p.states["AuthTest"]
	p.mu.Unlock()
	if state != FeedDegraded {
		t.Errorf("认证失败后状态 = %v，期望保持降级", state)
	}
}
