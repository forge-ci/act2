package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	log "github.com/sirupsen/logrus"
)

// The engine's BuildKit cache budget. Workflow `docker build`s reach the
// engine's own dockerd through the run proxies, which refuse /build/prune, so
// the build cache is shared by every run and owned by this engine alone. The
// server bounds it on its own: each pass first expires records unused for
// UnusedAge, then trims the rest to MaxBytes (least recently used first).
// BuildKit never removes a record a running build holds.

// BuilderBudget is the build-cache retention policy; MaxBytes 0 disables it.
type BuilderBudget struct {
	MaxBytes  int64
	UnusedAge time.Duration
	Interval  time.Duration
}

// BuilderPass is the last build-cache pass's outcome.
type BuilderPass struct {
	At        time.Time `json:"at"`
	Status    string    `json:"status"` // ok, failed
	Reclaimed uint64    `json:"reclaimed_bytes"`
	Error     string    `json:"error,omitempty"`
}

func (b BuilderBudget) validate() error {
	if b.MaxBytes < 0 || b.MaxBytes > 0 && (b.Interval < time.Minute || b.UnusedAge < time.Hour) {
		return errors.New("--builder-budget-max-bytes must not be negative; when set, its interval is at least 1m and its unused age at least 1h")
	}
	return nil
}

const builderPruneDeadline = 10 * time.Minute

type pruneReport struct {
	SpaceReclaimed uint64 `json:"SpaceReclaimed"`
}

// pruneQueries are the two prune requests of one pass: expiry, then the bound.
// keep-storage is the field older daemons read; reserved-space replaces it.
func (b BuilderBudget) pruneQueries() []url.Values {
	until, _ := json.Marshal(map[string][]string{"until": {b.UnusedAge.String()}})
	keep := fmt.Sprint(b.MaxBytes)
	return []url.Values{
		{"filters": {string(until)}},
		{"keep-storage": {keep}, "reserved-space": {keep}},
	}
}

func runBuilderPass(ctx context.Context, client *http.Client, b BuilderBudget) BuilderPass {
	pass := BuilderPass{At: time.Now().UTC(), Status: "ok"}
	for _, query := range b.pruneQueries() {
		reclaimed, err := pruneBuildCache(ctx, client, query)
		pass.Reclaimed += reclaimed
		if err != nil {
			pass.Status, pass.Error = "failed", err.Error()
			return pass
		}
	}
	return pass
}

func pruneBuildCache(ctx context.Context, client *http.Client, query url.Values) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, builderPruneDeadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/build/prune?"+query.Encode(), nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("build cache prune: %s: %s", resp.Status, body)
	}
	var report pruneReport
	if err := json.Unmarshal(body, &report); err != nil {
		return 0, fmt.Errorf("build cache prune reply: %w", err)
	}
	return report.SpaceReclaimed, nil
}

// runBuilderBudget applies the build-cache budget now and every interval.
func (s *Server) runBuilderBudget(ctx context.Context) {
	b := s.cfg.BuilderBudget
	if b.MaxBytes == 0 || s.cfg.DockerUpstream == "" {
		return
	}
	client := &http.Client{Transport: unixTransport(s.cfg.DockerUpstream)}
	for {
		pass := runBuilderPass(ctx, client, b)
		if pass.Status != "ok" {
			log.Warnf("build cache budget pass: %s %s", pass.Status, pass.Error)
		}
		s.mu.Lock()
		s.builder = &pass
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(b.Interval):
		}
	}
}
