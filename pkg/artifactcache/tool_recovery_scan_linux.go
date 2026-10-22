//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type toolScanMode uint8

const (
	toolScanRecovery toolScanMode = iota
	toolScanLegacyBootstrap
)

type toolRecoveryObjectPlan struct {
	path     string
	report   ToolSnapshotReport
	manifest toolManifest
	data     []byte
}

// Scan only namespace structure outside completed installs. Payload inspection
// happens once per completed install; its exact manifest is reused to publish.
// Both enumeration and retained manifests have finite aggregate bounds.
func inspectToolRecoveryUpdates(ctx context.Context, root, source string, original []ToolGenerationInstall, maxBytes int64) ([]toolRecoveryObjectPlan, error) {
	return inspectToolUpdates(ctx, root, source, original, maxBytes, toolScanRecovery)
}

func inspectToolUpdates(ctx context.Context, root, source string, original []ToolGenerationInstall, maxBytes int64, mode toolScanMode) ([]toolRecoveryObjectPlan, error) {
	if err := validateToolSnapshotPaths(source, root, maxBytes); err != nil {
		return nil, err
	}
	paths, err := completedToolPaths(ctx, source, mode)
	if err != nil {
		return nil, err
	}
	before := make(map[string]string, len(original))
	for _, install := range original {
		before[install.Path] = install.ObjectID
	}
	plans := make([]toolRecoveryObjectPlan, 0, len(paths))
	var totalBytes int64
	totalEntries, totalMetadata := 0, 0
	for _, path := range paths {
		report, manifest, data := inspectToolSnapshot(ctx, filepath.Join(source, path), root, maxBytes)
		if report.Partial {
			return nil, fmt.Errorf("recovery install %s: %s", path, report.Error)
		}
		if report.Bytes == nil || report.Entries == nil {
			return nil, fmt.Errorf("recovery install inventory missing")
		}
		if *report.Bytes > maxBytes-totalBytes {
			return nil, fmt.Errorf("recovery source exceeds complete payload bound")
		}
		totalBytes += *report.Bytes
		totalEntries += *report.Entries
		totalMetadata += len(data)
		if totalEntries > 100000 || totalMetadata > toolManifestLimit {
			return nil, fmt.Errorf("recovery install metadata exceeds aggregate bound")
		}
		// A stale lower's unchanged install is not an update. Replaying it into the
		// newest generation would discard another engine's completed installation.
		if before[path] != report.ID {
			plans = append(plans, toolRecoveryObjectPlan{path, report, manifest, data})
		}
	}
	return plans, nil
}

func completedToolRecoveryPaths(ctx context.Context, source string) ([]string, error) {
	return completedToolPaths(ctx, source, toolScanRecovery)
}

func completedToolPaths(ctx context.Context, source string, mode toolScanMode) ([]string, error) {
	var installs []string
	walked := 0
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		walked++
		if walked > 100000 {
			return fmt.Errorf("recovery namespace walk exceeds entry bound")
		}
		// The legacy seeder copies source/*: private root entries and save
		// stages never belong to the published tool-cache namespace.
		if isPrivateLegacyToolRoot(mode, source, path, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			if mode == toolScanLegacyBootstrap && entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("tool bootstrap namespace contains a symlink outside a completed install")
			}
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		depth := len(strings.Split(relative, string(filepath.Separator)))
		if relative == "." || depth < 2 {
			return nil
		}
		markers := []string{filepath.Join(path, ".complete")}
		if depth == 3 {
			markers = append(markers, path+".complete")
		}
		for _, marker := range markers {
			info, err := os.Lstat(marker)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("recovery completion marker is not regular")
			}
			if err := validateToolInstall(ToolGenerationInstall{Path: relative, ObjectID: strings.Repeat("0", 64)}); err != nil {
				return err
			}
			installs = append(installs, relative)
			if len(installs) > 256 {
				return fmt.Errorf("recovery complete install count exceeds bound")
			}
			return filepath.SkipDir
		}
		if depth > 48 {
			return fmt.Errorf("recovery namespace exceeds depth bound")
		}
		return nil
	})
	return installs, err
}

func isPrivateLegacyToolRoot(mode toolScanMode, source, path string, entry fs.DirEntry) bool {
	return mode == toolScanLegacyBootstrap && path != source && filepath.Dir(path) == source && strings.HasPrefix(entry.Name(), ".")
}
