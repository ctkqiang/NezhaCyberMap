package ringbuf

/*
本文件测试环形缓冲区的覆盖写、顺序快照与边界行为。
*/

import (
	"testing"
)

// TestRingPushOrder 验证容量内写入的顺序。
func TestRingPushOrder(t *testing.T) {
	r := New[int](5)
	for i := 1; i <= 3; i++ {
		r.Push(i)
	}
	got := r.Slice()
	want := []int{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("Slice() 长度 = %d，期望 %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Slice()[%d] = %d，期望 %d", i, got[i], want[i])
		}
	}
}

// TestRingOverwrite 验证写满后覆盖最旧条目。
func TestRingOverwrite(t *testing.T) {
	r := New[int](3)
	for i := 1; i <= 5; i++ {
		r.Push(i)
	}
	if r.Len() != 3 {
		t.Fatalf("Len() = %d，期望 3", r.Len())
	}
	got := r.Slice()
	want := []int{3, 4, 5}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Slice()[%d] = %d，期望 %d（覆盖写顺序错误）", i, got[i], want[i])
		}
	}
	last, ok := r.Last()
	if !ok || last != 5 {
		t.Fatalf("Last() = %d, %v，期望 5, true", last, ok)
	}
}

// TestRingEmpty 验证空缓冲区行为。
func TestRingEmpty(t *testing.T) {
	r := New[int](4)
	if r.Len() != 0 {
		t.Fatalf("Len() = %d，期望 0", r.Len())
	}
	if _, ok := r.Last(); ok {
		t.Error("空缓冲区 Last(): 期望 ok=false")
	}
	if len(r.Slice()) != 0 {
		t.Error("空缓冲区 Slice(): 期望空切片")
	}
}

// TestRingClear 验证清空行为。
func TestRingClear(t *testing.T) {
	r := New[int](3)
	r.Push(1)
	r.Push(2)
	r.Clear()
	if r.Len() != 0 {
		t.Fatalf("Clear 后 Len() = %d，期望 0", r.Len())
	}
	r.Push(7)
	got := r.Slice()
	if len(got) != 1 || got[0] != 7 {
		t.Fatalf("Clear 后重新写入异常: %v", got)
	}
}

// TestRingCapGuard 验证非法容量被纠正为 1。
func TestRingCapGuard(t *testing.T) {
	r := New[int](0)
	if r.Cap() != 1 {
		t.Fatalf("Cap() = %d，期望 1", r.Cap())
	}
}
