//go:build linux

package artifactcache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestToolCopyFailureCancelsAndDrainsBoundedWorkers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan string, 100)
	draining := make(chan struct{}, 4)
	fail, allowDrain := make(chan struct{}), make(chan struct{})
	releaseFailure := sync.OnceFunc(func() { close(fail) })
	releaseDrain := sync.OnceFunc(func() { close(allowDrain) })
	defer releaseFailure()
	defer releaseDrain()
	expected := errors.New("injected file finalization failure")
	entries := make([]toolEntry, 100)
	for index := range entries {
		entries[index] = toolEntry{Path: fmt.Sprintf("%02d", index), Kind: "file"}
	}
	result := make(chan error, 1)
	go func() {
		result <- copyToolSnapshotEntries(ctx, "/source", "/stage", entries, func(copyCtx context.Context, _, _ string, entry toolEntry) error {
			if err := copyCtx.Err(); err != nil {
				return err
			}
			started <- entry.Path
			if entry.Path == "00" {
				<-fail
				return expected
			}
			<-copyCtx.Done()
			draining <- struct{}{}
			<-allowDrain
			return copyCtx.Err()
		})
	}()
	for index := 0; index < 4; index++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("copy workers did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("more than four copies admitted before a worker finished")
	default:
	}
	releaseFailure()
	for index := 0; index < 3; index++ {
		select {
		case <-draining:
		case <-ctx.Done():
			t.Fatal("copy failure did not cancel other workers")
		}
	}
	select {
	case <-result:
		t.Fatal("stage owner returned before active copies drained")
	default:
	}
	releaseDrain()
	select {
	case err := <-result:
		require.ErrorIs(t, err, expected)
	case <-ctx.Done():
		t.Fatal("copy workers did not drain")
	}
}
