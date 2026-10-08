// Package doctor runs the brain's health checks. Each area adds its checks from its own file with Register.
package doctor

import (
	"context"
	"sort"
	"sync"
	"time"
)

type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
	Skip Status = "skip" // the part is not installed on this machine (yet)
)

type Result struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Ms     int64  `json:"ms"`
}

type Check struct {
	Name string
	Run  func(ctx context.Context) (Status, string)
}

var (
	mu     sync.Mutex
	checks []Check
)

func Register(c Check) {
	mu.Lock()
	defer mu.Unlock()
	checks = append(checks, c)
}

// RunAll runs every check concurrently, each bounded by timeout, and returns them sorted by name.
func RunAll(ctx context.Context, timeout time.Duration) []Result {
	mu.Lock()
	list := append([]Check(nil), checks...)
	mu.Unlock()
	results := make([]Result, len(list))
	var wg sync.WaitGroup
	for i, c := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			started := time.Now()
			done := make(chan Result, 1)
			go func() {
				st, detail := c.Run(cctx)
				done <- Result{Name: c.Name, Status: st, Detail: detail}
			}()
			select {
			case r := <-done:
				results[i] = r
			case <-cctx.Done():
				results[i] = Result{Name: c.Name, Status: Fail, Detail: "timed out after " + timeout.String()}
			}
			results[i].Ms = time.Since(started).Milliseconds()
		}()
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results
}

// Green is true when no check failed or warned; skips count as green only when allowSkip.
func Green(results []Result, allowSkip bool) bool {
	for _, r := range results {
		if r.Status == Fail || r.Status == Warn || (r.Status == Skip && !allowSkip) {
			return false
		}
	}
	return true
}
