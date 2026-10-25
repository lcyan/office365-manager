package auth

import (
	"sync"
	"time"
)

// Limiter 是按 key（登录场景为客户端 IP）的令牌桶限速器。
// capacity 为突发容量，refill 每 refill 间隔恢复 1 个令牌。
type Limiter struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	capacity  float64
	refill    float64 // 每秒恢复的令牌数
	lastPrune time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter 构造限速器。capacity 个突发配额，之后每 refill 恢复 1 个。
func NewLimiter(capacity int, refill time.Duration) *Limiter {
	return &Limiter{
		buckets:  make(map[string]*bucket),
		capacity: float64(capacity),
		refill:   1 / refill.Seconds(),
	}
}

// Allow 消耗 1 个令牌；不足时返回 false。
func (l *Limiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	l.pruneLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		l.buckets[key] = &bucket{tokens: l.capacity - 1, last: now}
		return true
	}
	b.tokens += now.Sub(b.last).Seconds() * l.refill
	if b.tokens > l.capacity {
		b.tokens = l.capacity
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// pruneLocked 清理超过 1 小时未使用的桶，防止内存无限增长。
func (l *Limiter) pruneLocked(now time.Time) {
	if now.Sub(l.lastPrune) < time.Hour {
		return
	}
	for key, b := range l.buckets {
		if now.Sub(b.last) > time.Hour {
			delete(l.buckets, key)
		}
	}
	l.lastPrune = now
}
