//go:build linux

package artifactcache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const toolRecoveryCompletionDirectory = ".tool-recovery-completions-v1"
const toolRecoveryCompletionBytes = 2048

func (c ToolRecoveryCompletion) validate() error {
	if err := c.Pin.validate(); err != nil {
		return err
	}
	if !canonicalRecoveryID(c.Generation) || c.CompletedAt.Before(c.Pin.CreatedAt) || !c.CompletedAt.Before(c.Pin.ExpiresAt) {
		return fmt.Errorf("recovery completion identity or timestamp is invalid")
	}
	return nil
}

func sameToolRecoveryPin(a, b ToolRecoveryPin) bool {
	first, firstErr := json.Marshal(a)
	second, secondErr := json.Marshal(b)
	return firstErr == nil && secondErr == nil && bytes.Equal(first, second)
}

// The caller holds the original catalog writer. A selection's embedded receipt
// survives a crash between selecting its successor and acknowledging the reply.
// Every successor archives that receipt durably before replacing the pointer.
func completedToolRecovery(root string, selection ToolGenerationSelection, pin ToolRecoveryPin) (*ToolRecoveryCompletion, error) {
	if selection.Recovery != nil && selection.Recovery.Pin.Owner == pin.Owner {
		if !sameToolRecoveryPin(selection.Recovery.Pin, pin) {
			return nil, fmt.Errorf("recovery completion differs from frozen intent")
		}
		receipt := *selection.Recovery
		return &receipt, nil
	}
	directory := filepath.Join(root, toolRecoveryCompletionDirectory)
	if info, err := os.Lstat(directory); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("recovery completion namespace is invalid")
	}
	if err := verifyToolRecoveryCompletionDirectory(root, directory); err != nil {
		return nil, err
	}
	completion, err := readToolRecoveryCompletion(filepath.Join(directory, pin.Owner+".json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !sameToolRecoveryPin(completion.Pin, pin) {
		return nil, fmt.Errorf("recovery completion differs from frozen intent")
	}
	return &completion, nil
}

func readToolRecoveryCompletion(path string) (ToolRecoveryCompletion, error) {
	var completion ToolRecoveryCompletion
	info, err := os.Lstat(path)
	if err != nil {
		return completion, err
	}
	if !info.Mode().IsRegular() || info.Size() > toolRecoveryCompletionBytes {
		return completion, fmt.Errorf("recovery completion must be a bounded regular record")
	}
	file, err := os.Open(path)
	if err != nil {
		return completion, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return completion, fmt.Errorf("recovery completion identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, toolRecoveryCompletionBytes+1))
	if err != nil || len(data) > toolRecoveryCompletionBytes {
		return completion, fmt.Errorf("recovery completion exceeds read bound")
	}
	if err := json.Unmarshal(data, &completion); err != nil {
		return completion, err
	}
	canonical, err := json.Marshal(completion)
	if err != nil || !bytes.Equal(data, canonical) {
		return completion, fmt.Errorf("recovery completion is not canonical")
	}
	return completion, completion.validate()
}

func prepareToolRecoveryCompletionDirectory(root string) (string, error) {
	directory := filepath.Join(root, toolRecoveryCompletionDirectory)
	if err := os.Mkdir(directory, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := verifyToolRecoveryCompletionDirectory(root, directory); err != nil {
		return "", err
	}
	if err := syncToolDirectory(root); err != nil {
		return "", err
	}
	return directory, nil
}

// Reconciliation history is bounded by the same finite pin lifetime and record
// ceiling. Each write expires at most 32 old receipts; overflow refuses further
// publication rather than making an unbounded log or dropping a live receipt.
func boundToolRecoveryCompletions(ctx context.Context, directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	entries, err := handle.ReadDir(toolRecoveryPinLimit + 1)
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) > toolRecoveryPinLimit {
		return fmt.Errorf("recovery completion inventory exceeds bound")
	}
	removed := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(directory, entry.Name())
		c, err := readToolRecoveryCompletion(path)
		if err != nil {
			return err
		}
		if entry.Name() != c.Pin.Owner+".json" {
			return fmt.Errorf("recovery completion filename differs from owner")
		}
		if removed < 32 && !c.Pin.ExpiresAt.After(time.Now().UTC()) {
			if err := os.Remove(path); err != nil {
				return err
			}
			removed++
		}
	}
	if err := syncToolDirectory(directory); err != nil {
		return err
	}
	if len(entries)-removed >= toolRecoveryPinLimit {
		return fmt.Errorf("recovery completion inventory is full")
	}
	return nil
}

func archiveToolRecoveryCompletion(ctx context.Context, catalog transferLease, root string, completion ToolRecoveryCompletion) (err error) {
	if err := completion.validate(); err != nil {
		return err
	}
	directory, err := prepareToolRecoveryCompletionDirectory(root)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, completion.Pin.Owner+".json")
	if existing, readErr := readToolRecoveryCompletion(path); readErr == nil {
		if !sameToolRecoveryPin(existing.Pin, completion.Pin) || existing.Generation != completion.Generation || !existing.CompletedAt.Equal(completion.CompletedAt) {
			return fmt.Errorf("recovery completion is immutable")
		}
		return syncToolDirectory(directory)
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	if err := boundToolRecoveryCompletions(ctx, directory); err != nil {
		return err
	}
	data, err := json.Marshal(completion)
	if err != nil || len(data) > toolRecoveryCompletionBytes {
		return fmt.Errorf("recovery completion encoding exceeds bound")
	}
	stage, err := createOwnedToolStage(catalog, root, root, ".tool-stage-")
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := cleanupOwnedToolStage(ctx, catalog, root, stage); cleanupErr != nil {
			err = fmt.Errorf("recovery receipt stage cleanup: %w", cleanupErr)
		}
	}()
	staged := filepath.Join(stage, "completion.json")
	file, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := syncToolDirectory(stage); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(staged, path); err != nil {
		return err
	}
	return syncToolDirectory(directory)
}

func archiveSelectedToolRecovery(ctx context.Context, catalog transferLease, root string, selection ToolGenerationSelection, readErr error) error {
	if readErr != nil || selection.Recovery == nil {
		return nil
	}
	return archiveToolRecoveryCompletion(ctx, catalog, root, *selection.Recovery)
}

func verifyToolRecoveryCompletionDirectory(root, directory string) error {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("recovery completion namespace is invalid")
	}
	rootMount, err := toolMountID(root)
	if err != nil {
		return err
	}
	mount, err := toolMountID(directory)
	if err != nil || mount != rootMount {
		return fmt.Errorf("recovery completion namespace crosses mount boundary")
	}
	return nil
}
