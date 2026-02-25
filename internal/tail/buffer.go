package tail

import "sync"

// Buffer stores the tail of recently added lines in a bounded ring.
type Buffer struct {
	mu    sync.Mutex
	lines []string
	next  int
	count int
}

// NewBuffer creates a tail buffer with capacity for up to size lines.
func NewBuffer(size int) *Buffer {
	if size <= 0 {
		size = 1
	}
	return &Buffer{lines: make([]string, size)}
}

// Add appends a line, evicting the oldest line if the buffer is full.
func (b *Buffer) Add(line string) {
	b.mu.Lock()
	b.lines[b.next] = line
	b.next = (b.next + 1) % len(b.lines)
	if b.count < len(b.lines) {
		b.count++
	}
	b.mu.Unlock()
}

// Lines returns buffered lines in oldest-to-newest order.
func (b *Buffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.count == 0 {
		return nil
	}

	out := make([]string, 0, b.count)
	start := (b.next - b.count + len(b.lines)) % len(b.lines)
	for i := 0; i < b.count; i++ {
		out = append(out, b.lines[(start+i)%len(b.lines)])
	}
	return out
}
