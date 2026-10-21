package serve

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const toolcacheReadyLimit = 4096

// The preparation stamp freezes the complete native producer binding. A
// generation ID alone cannot prove its store or overlay recipe on restart.
type toolcacheReady struct {
	SchemaVersion int             `json:"schema_version"`
	Mode          string          `json:"mode"`
	Generation    *ToolGeneration `json:"generation,omitempty"`
}

func (s *Server) toolcacheReady(generation *ToolGeneration) (toolcacheReady, error) {
	ready := toolcacheReady{SchemaVersion: 1, Mode: "seeded"}
	if generation == nil {
		return ready, nil
	}
	if !generationID.MatchString(generation.ID) || !filepath.IsAbs(generation.Store) {
		return ready, errors.New("tool generation: invalid id or store")
	}
	frozen := *generation
	frozen.OverlayRecipeSHA256 = strings.TrimPrefix(frozen.OverlayRecipeSHA256, "sha256:")
	if digestOf([]byte(OverlayRecipe(frozen.Store, s.cfg.ToolCacheMount))) != "sha256:"+frozen.OverlayRecipeSHA256 {
		return ready, errors.New("tool overlay recipe differs from frozen producer")
	}
	ready.Mode, ready.Generation = "generation", &frozen
	return ready, nil
}

func (ready toolcacheReady) description() string {
	if ready.Generation != nil {
		return "generation " + ready.Generation.ID
	}
	return ready.Mode
}

func (ready toolcacheReady) matches(wanted toolcacheReady) bool {
	if ready.SchemaVersion != wanted.SchemaVersion || ready.Mode != wanted.Mode {
		return false
	}
	if ready.Generation == nil || wanted.Generation == nil {
		return ready.Generation == nil && wanted.Generation == nil
	}
	return *ready.Generation == *wanted.Generation
}

func (s *Server) preparedToolcache() (toolcacheReady, error) {
	ready, err := readToolcacheReady(s.readyStamp())
	if err != nil {
		return ready, err
	}
	wanted, err := s.toolcacheReady(ready.Generation)
	if err != nil {
		return ready, err
	}
	if !ready.matches(wanted) {
		return ready, errors.New("invalid frozen preparation binding")
	}
	return ready, nil
}

func readToolcacheReady(path string) (toolcacheReady, error) {
	var ready toolcacheReady
	info, err := os.Lstat(path)
	if err != nil {
		return ready, err
	}
	if !info.Mode().IsRegular() || info.Size() > toolcacheReadyLimit {
		return ready, errors.New("readiness must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return ready, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return ready, errors.New("readiness identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, toolcacheReadyLimit+1))
	if err != nil {
		return ready, err
	}
	if len(data) > toolcacheReadyLimit {
		return ready, errors.New("readiness exceeds bound")
	}
	// Historical seeded engines carry no native producer authority. A legacy
	// "generation ID" stamp lacks store/recipe evidence and must refuse.
	if string(data) == "seeded" {
		return toolcacheReady{SchemaVersion: 1, Mode: "seeded"}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ready); err != nil {
		return ready, fmt.Errorf("invalid frozen preparation binding: %w", err)
	}
	if err := decoder.Decode(new(struct{})); err != io.EOF {
		return ready, errors.New("readiness has trailing data")
	}
	return ready, nil
}
