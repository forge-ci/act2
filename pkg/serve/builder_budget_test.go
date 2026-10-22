package serve

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeDockerd(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "d.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return &http.Client{Transport: unixTransport(socket)}
}

func TestBuilderPassExpiresThenBoundsTheBuildCache(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	client := fakeDockerd(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"CachesDeleted":["a"],"SpaceReclaimed":100}`))
	})
	b := BuilderBudget{MaxBytes: 8 << 30, UnusedAge: 168 * time.Hour, Interval: time.Hour}
	pass := runBuilderPass(context.Background(), client, b)
	assert.Equal(t, "ok", pass.Status)
	assert.EqualValues(t, 200, pass.Reclaimed)
	require.Len(t, seen, 2)
	assert.Equal(t, `POST /build/prune?filters=%7B%22until%22%3A%5B%22168h0m0s%22%5D%7D`, seen[0])
	assert.Equal(t, `POST /build/prune?keep-storage=8589934592&reserved-space=8589934592`, seen[1])
}

func TestBuilderPassReportsARefusedPruneAndStops(t *testing.T) {
	calls := 0
	client := fakeDockerd(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, `{"message":"builder busy"}`, http.StatusInternalServerError)
	})
	pass := runBuilderPass(context.Background(), client, BuilderBudget{MaxBytes: 1, UnusedAge: time.Hour})
	assert.Equal(t, "failed", pass.Status)
	assert.Contains(t, pass.Error, "builder busy")
	assert.Equal(t, 1, calls)
}

func TestBuilderBudgetValidation(t *testing.T) {
	assert.NoError(t, BuilderBudget{}.validate())
	assert.NoError(t, BuilderBudget{MaxBytes: 1, UnusedAge: time.Hour, Interval: time.Minute}.validate())
	assert.Error(t, BuilderBudget{MaxBytes: -1}.validate())
	assert.Error(t, BuilderBudget{MaxBytes: 1, UnusedAge: time.Hour, Interval: time.Second}.validate())
}
