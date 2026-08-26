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
	cached     Report
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
		if len(c.cached.Profiles) > 0 && now.Before(c.expires) {
			report := c.cached
			report.Cached = true
			expires := c.expires
			report.ExpiresAt = &expires
			c.mu.Unlock()
			return report, nil
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

		probeCtx, cancel := context.WithTimeout(ctx, c.timeout)
		report, err := c.runner.Run(probeCtx, c.runner.Profiles())
		cancel()

		c.mu.Lock()
		if err == nil {
			c.cached = report
			c.expires = c.now().Add(c.ttl)
		}
		expires := c.expires
		c.refreshing = false
		close(wait)
		c.mu.Unlock()
		if err != nil {
			return Report{}, err
		}
		report.ExpiresAt = &expires
		return report, nil
	}
}
