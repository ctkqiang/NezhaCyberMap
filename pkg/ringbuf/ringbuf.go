/*
Package ringbuf 实现固定容量的环形缓冲区。

用于 TUI 侧缓存最近 N 条攻击事件（默认 500）。缓冲区写满后覆盖最旧条目，
保证内存占用有界，符合 agent.md 第 5 节「禁止 append 无界增长」的约束。
环形缓冲区不是并发安全的：TUI 侧仅在 Update 单线程中读写；摄入侧通过
channel 与 TUI 通信，不直接触碰本结构。
*/
package ringbuf

/*
Ring 是一个固定容量的泛型环形缓冲区。

类型参数:
  - T: 存储元素的类型。
*/
type Ring[T any] struct {
	buf  []T
	head int // 下一个可写位置
	size int // 当前元素个数
	cap  int
}

/*
New 创建一个容量为 cap 的环形缓冲区。

参数:
  - cap (int): 最大容量，必须 >= 1。

返回:
  - *Ring[T]: 空缓冲区实例。
*/
func New[T any](cap int) *Ring[T] {
	if cap < 1 {
		cap = 1
	}
	return &Ring[T]{buf: make([]T, cap), cap: cap}
}

/*
Push 写入一个新元素。缓冲区满时覆盖最旧的元素。

参数:
  - v (T): 待写入元素。
*/
func (r *Ring[T]) Push(v T) {
	r.buf[r.head] = v
	r.head = (r.head + 1) % r.cap
	if r.size < r.cap {
		r.size++
	}
}

/*
Slice 按时间顺序（最旧 → 最新）返回当前全部元素的快照。

返回:
  - []T: 元素快照，调用方不得修改。
*/
func (r *Ring[T]) Slice() []T {
	out := make([]T, r.size)
	start := (r.head - r.size + r.cap) % r.cap
	for i := 0; i < r.size; i++ {
		out[i] = r.buf[(start+i)%r.cap]
	}
	return out
}

/*
Last 返回最新的元素。

返回:
  - v (T): 最新元素。
  - ok (bool): 缓冲区为空时返回 false。
*/
func (r *Ring[T]) Last() (v T, ok bool) {
	if r.size == 0 {
		return v, false
	}
	idx := (r.head - 1 + r.cap) % r.cap
	return r.buf[idx], true
}

/*
Len 返回当前元素个数。

返回:
  - int: 元素个数，范围 [0, cap]。
*/
func (r *Ring[T]) Len() int {
	return r.size
}

/*
Cap 返回缓冲区容量。

返回:
  - int: 创建时指定的容量。
*/
func (r *Ring[T]) Cap() int {
	return r.cap
}

/*
Clear 清空缓冲区（不释放底层数组）。

返回:
  - 无。
*/
func (r *Ring[T]) Clear() {
	r.head = 0
	r.size = 0
}
