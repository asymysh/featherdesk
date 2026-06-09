package server

import (
	"sync"
	"time"
)

// rateLimiter is a simple token-bucket rate limiter.
// rate is tokens added per second; burst is the bucket capacity.
type rateLimiter struct {
	mu     sync.Mutex
	tokens float64
	burst  float64
	rate   float64
	last   time.Time
}

func newRateLimiter(rate, burst float64) *rateLimiter {
	return &rateLimiter{
		tokens: burst,
		burst:  burst,
		rate:   rate,
		last:   time.Now(),
	}
}

// allow reports whether one event may proceed, consuming a token if so.
func (r *rateLimiter) allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	r.tokens += now.Sub(r.last).Seconds() * r.rate
	if r.tokens > r.burst {
		r.tokens = r.burst
	}
	r.last = now

	if r.tokens < 1 {
		return false
	}
	r.tokens--
	return true
}
