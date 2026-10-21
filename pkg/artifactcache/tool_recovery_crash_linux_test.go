//go:build linux

package artifactcache

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recoveryHelperFixture struct{ Root, Source, Record, Ready string }

func TestToolRecoveryPublicationSurvivesLostHelperReply(t *testing.T) {
	if os.Getenv("ACT_TEST_RECOVERY_NO_REPLY") == "1" {
		var fixture recoveryHelperFixture
		require.NoError(t, json.NewDecoder(os.Stdin).Decode(&fixture))
		data, err := os.ReadFile(fixture.Record)
		require.NoError(t, err)
		var pin ToolRecoveryPin
		require.NoError(t, json.Unmarshal(data, &pin))
		report := PublishToolRecovery(context.Background(), fixture.Root, fixture.Source, pin, 100)
		require.False(t, report.Partial, report.Error)
		require.True(t, report.Completed)
		data, err = json.Marshal(report)
		require.NoError(t, err)
		// This private test rendezvous is not a reply to the source's coordinator.
		require.NoError(t, os.WriteFile(fixture.Ready, data, 0600))
		time.Sleep(30 * time.Second)
		t.Fatal("parent failed to terminate withheld reply helper")
	}
	root, source, pin := toolRecoveryPublicationFixture(t)
	dir := t.TempDir()
	record, ready := filepath.Join(dir, "pin.json"), filepath.Join(dir, "ready.json")
	data, err := json.Marshal(pin)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(record, data, 0600))
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "-test.run", "^TestToolRecoveryPublicationSurvivesLostHelperReply$")
	child.Env = append(os.Environ(), "ACT_TEST_RECOVERY_NO_REPLY=1")
	fixtureData, err := json.Marshal(recoveryHelperFixture{Root: root, Source: source, Record: record, Ready: ready})
	require.NoError(t, err)
	child.Stdin = bytes.NewReader(fixtureData)
	require.NoError(t, child.Start())
	waited := false
	defer func() {
		if !waited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	require.Eventually(t, func() bool { info, err := os.Stat(ready); return err == nil && info.Size() > 0 }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, child.Process.Kill())
	require.Error(t, child.Wait(), "helper was killed before it returned its reply")
	waited = true
	data, err = os.ReadFile(ready)
	require.NoError(t, err)
	var first ToolRecoveryPublicationReport
	require.NoError(t, json.Unmarshal(data, &first))
	require.NoError(t, os.Rename(source, source+"-unmounted"))
	original, _, err := readToolGenerationManifest(filepath.Join(root, toolGenerationDirectory, pin.Generation), pin.Generation)
	require.NoError(t, err)
	later := UpdateToolGeneration(context.Background(), root, ToolGenerationSpec{SchemaVersion: 1, Installs: []ToolGenerationInstall{{Path: "Node/2/x64", ObjectID: original.Installs[0].ObjectID}}}, 100)
	require.False(t, later.Partial, later.Error)
	retried := PublishToolRecovery(context.Background(), root, source, pin, 100)
	require.Equal(t, first, retried, "lost helper reply reconciles without a source or another publication")
	current, err := CurrentToolGeneration(context.Background(), root, 100)
	require.NoError(t, err)
	require.Equal(t, later.Generation.ID, current.ID)
}
