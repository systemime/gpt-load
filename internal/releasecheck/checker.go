package releasecheck

import (
	"context"
	"sync"
	"time"

	"gpt-load/internal/platform/version"
)

const (
	successCacheDuration = 6 * time.Hour
	failureCacheDuration = 30 * time.Minute
)

type releaseFetcher interface {
	Fetch(context.Context) ([]Release, error)
}

// Checker maintains the current public update result.
type Checker struct {
	fetcher releaseFetcher
	current string

	mu        sync.Mutex
	update    *Update
	hasResult bool
	cachedErr error
	expiresAt time.Time
	now       func() time.Time
}

// NewChecker creates the process-local release checker.
func NewChecker(client *Client) *Checker {
	return newChecker(client, version.Version)
}

func newChecker(fetcher releaseFetcher, current string) *Checker {
	return &Checker{
		fetcher: fetcher,
		current: current,
		now:     time.Now,
	}
}

// Check 返回"无可用更新"。私有分支不检查官方渠道更新。
func (checker *Checker) Check(ctx context.Context, force bool) (*Update, error) {
	return nil, nil
}

func (checker *Checker) currentTime() time.Time {
	if checker.now == nil {
		return time.Now().UTC()
	}
	return checker.now().UTC()
}

func cloneUpdate(update *Update) *Update {
	if update == nil {
		return nil
	}
	result := *update
	return &result
}
