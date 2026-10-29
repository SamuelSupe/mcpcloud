package probe

import (
	"context"
	"sync"
	"time"
)

type ReportRunner interface {
	Profiles() []string
	Run(context.Context, []string) (Report, error)
}

type Coordinator struct {
	runner  ReportRunner
	ttl     time.Duration
	timeout time.Duration
	now     func() time.Time

	mu         sync.Mutex
	hasCached  bool
	cached     Report
	cachedErr  error
	expires    time.Time
	refreshing bool
	wait       chan struct{}
}

func NewCoordinator(runner ReportRunner, ttl, timeout time.Duration) *Coordinator {
	if ttl <= 0 {
		ttl = time.Minute
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Coordinator{runner: runner, ttl: ttl, timeout: timeout, now: time.Now}
}

func (c *Coordinator) Current(ctx context.Context) (Report, error) {
	for {
		c.mu.Lock()
		now := c.now()
		if c.hasCached && now.Before(c.expires) {
			report := c.cached
			report.Cached = true
			expires := c.expires
			report.ExpiresAt = &expires
			err := c.cachedErr
			c.mu.Unlock()
			return report, err
		}
		if c.refreshing {
			wait := c.wait
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return Report{}, ctx.Err()
			case <-wait:
				continue
			}
		}
		c.refreshing = true
		c.wait = make(chan struct{})
		wait := c.wait
		c.mu.Unlock()

		// A shared refresh must outlive the initiating HTTP request, but stays bounded.
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
		report, err := c.runner.Run(probeCtx, c.runner.Profiles())
		cancel()

		c.mu.Lock()
		c.cached = report
		c.cachedErr = err
		c.hasCached = true
		c.expires = c.now().Add(c.ttl)
		expires := c.expires
		c.refreshing = false
		close(wait)
		c.mu.Unlock()
		report.ExpiresAt = &expires
		return report, err
	}
}
