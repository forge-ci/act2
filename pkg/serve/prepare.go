package serve

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Engine preparation the server owns: the pinned runner image, loaded and
// proved once per engine, and the tool cache (act's act-toolcache volume at
// /opt/hostedtoolcache in every job container), seeded once per engine from
// the shared store and saved back after each cleanly finished run.

var (
	//go:embed scripts/runner_image.sh
	runnerImageScript string
	//go:embed scripts/toolcache_seed.sh
	toolcacheSeedScript string
	//go:embed scripts/toolcache_overlay.sh
	toolcacheOverlayRecipe string
	//go:embed scripts/toolcache_save.sh
	toolcacheSaveScript string
)

// StaleStageMinutes: a save stage older than this was abandoned by a killed
// save; the next save removes it.
const StaleStageMinutes = 30

// ToolGeneration selects an immutable tool generation, mounted read-only
// under a private overlay instead of copying the store.
type ToolGeneration struct {
	ID string `json:"id"`
	// Store is the generation store holding .tool-generations-v1/<id>.
	Store string `json:"store"`
	// OverlayRecipeSHA256 is the recipe the generation was frozen for.
	OverlayRecipeSHA256 string `json:"overlay_recipe_sha256"`
}

// PrepareRequest prepares the engine before runs are admitted. Each part is
// idempotent; a part already done is only re-verified.
type PrepareRequest struct {
	Image      *ImagePin       `json:"image,omitempty"`
	Generation *ToolGeneration `json:"tool_generation,omitempty"`
}

// PrepareReply reports what is prepared.
type PrepareReply struct {
	Image     *ImageState `json:"image,omitempty"`
	ToolCache string      `json:"tool_cache,omitempty"`
}

// ToolCacheSave reports a close's tool-cache save.
type ToolCacheSave struct {
	Saved bool   `json:"saved"`
	Note  string `json:"note"`
}

// CloseReply is a close's result.
type CloseReply struct {
	ToolCache *ToolCacheSave `json:"tool_cache,omitempty"`
}

var generationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// OverlayRecipe is the exact overlay script for a store and target; its
// digest is frozen into each tool generation.
func OverlayRecipe(store, target string) string {
	return strings.NewReplacer("@STORE@", store, "@TARGET@", target, "@UPPER@", path.Join(nativeToolSourceRoot, nativeToolUpperName), "@WORK@", path.Join(nativeToolSourceRoot, nativeToolWorkName)).Replace(toolcacheOverlayRecipe)
}

func (s *Server) toolcacheEnabled() bool { return s.cfg.ToolCacheStore != "" }

func (s *Server) toolcacheEnv() []string {
	return []string{
		"ACT_TOOLCACHE_STORE=" + s.cfg.ToolCacheStore,
		"ACT_TOOLCACHE_VOLUME=" + s.cfg.ToolCacheVolume,
		"ACT_TOOLCACHE_MOUNT=" + s.cfg.ToolCacheMount,
		fmt.Sprintf("ACT_STALE_STAGE_MINUTES=%d", StaleStageMinutes),
	}
}

// shell runs script with env added, returning stdout or an error with the
// tail of stderr.
func shell(ctx context.Context, script string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", script) //nolint:gosec,nolintlint // scripts are this package's embedded constants; values travel in env
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		tail := strings.TrimSpace(stderr.String())
		if len(tail) > 2048 {
			tail = tail[len(tail)-2048:]
		}
		return stdout.Bytes(), fmt.Errorf("%w: %s", err, tail)
	}
	return stdout.Bytes(), nil
}

// loadImage makes pin's tag present and proved: loaded from its archive, or
// pulled once and saved there; an image that fails the proof is dropped and
// pulled again once.
func (s *Server) loadImage(ctx context.Context, pin ImagePin) (*ImageState, error) {
	env := []string{"ACT_IMAGE_TAG=" + pin.Tag, "ACT_IMAGE_ARCHIVE=" + pin.Archive,
		"ACT_IMAGE_REFERENCE=" + pin.Reference, "ACT_IMAGE_PLATFORM=" + pin.Platform}
	var refused error
	for _, reload := range []string{"0", "1"} {
		if inspect, err := shell(ctx, "docker image inspect \"$ACT_IMAGE_TAG\"", env); err == nil && reload == "0" {
			// Already present: proved without touching the archive.
			if id, err := VerifyLoadedImage(inspect, pin); err == nil {
				return &ImageState{Tag: pin.Tag, ID: id}, nil
			}
		}
		if _, err := shell(ctx, runnerImageScript, append(env, "ACT_IMAGE_RELOAD="+reload)); err != nil {
			return nil, fmt.Errorf("runner image: %w", err)
		}
		inspect, err := shell(ctx, "docker image inspect \"$ACT_IMAGE_TAG\"", env)
		if err != nil {
			return nil, fmt.Errorf("runner image inspect: %w", err)
		}
		id, err := VerifyLoadedImage(inspect, pin)
		if err == nil {
			return &ImageState{Tag: pin.Tag, ID: id}, nil
		}
		refused = fmt.Errorf("runner image is not the pinned one: %w", err)
	}
	return nil, refused
}

// readyStamp records that the engine's tool cache is prepared; it lives next
// to the socket, on the engine's storage, so a restarted server keeps it.
func (s *Server) readyStamp() string {
	return filepath.Join(filepath.Dir(s.cfg.Socket), "toolcache.ready")
}

// prepareToolcache seeds the tool-cache volume, or mounts a generation's
// overlay, once per engine.
func (s *Server) prepareToolcache(ctx context.Context, generation *ToolGeneration) (string, error) {
	wanted, err := s.toolcacheReady(generation)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(wanted)
	if err != nil || len(encoded) > toolcacheReadyLimit {
		return "", errors.New("tool cache readiness exceeds bound")
	}
	if done, err := s.preparedToolcache(); err == nil {
		if !done.matches(wanted) {
			return "", errors.New("tool cache is already prepared for a different frozen binding")
		}
		return wanted.description(), nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("tool cache readiness: %w", err)
	}
	script := toolcacheSeedScript
	if wanted.Generation != nil {
		recipe := OverlayRecipe(wanted.Generation.Store, s.cfg.ToolCacheMount)
		script = strings.ReplaceAll(recipe, "@GENERATION@", wanted.Generation.ID)
	}
	if _, err := shell(ctx, script, s.toolcacheEnv()); err != nil {
		return "", fmt.Errorf("tool cache: %w", err)
	}
	return wanted.description(), os.WriteFile(s.readyStamp(), encoded, 0o600)
}

func (s *Server) prepare(w http.ResponseWriter, r *http.Request) {
	var req PrepareRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if req.Image != nil {
		if err := req.Image.Validate(); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
	}
	if req.Generation != nil && !s.toolcacheEnabled() {
		fail(w, http.StatusBadRequest, errors.New("tool generation: the server has no --toolcache-store"))
		return
	}
	var out PrepareReply
	if req.Image != nil {
		s.imageMu.Lock()
		image, err := s.loadImage(r.Context(), *req.Image)
		s.imageMu.Unlock()
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		out.Image = image
		s.mu.Lock()
		s.image = image
		s.mu.Unlock()
	}
	if s.toolcacheEnabled() {
		s.toolMu.Lock()
		mode, err := s.prepareToolcache(r.Context(), req.Generation)
		s.toolMu.Unlock()
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		out.ToolCache = mode
	}
	reply(w, http.StatusOK, out)
}

// saveToolcache keeps a cleanly finished run's new installs. Best effort: a
// failed save is reported and never fails the close.
func (s *Server) saveToolcache(ctx context.Context) *ToolCacheSave {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	ready, err := s.preparedToolcache()
	if err != nil {
		return &ToolCacheSave{Note: "tool cache readiness: " + err.Error()}
	}
	if ready.Generation != nil {
		// Other admitted runs may still write this overlay. The legacy saver has
		// neither native catalog authority nor a source-stop proof. Keep the source
		// for the owner's post-stop native recovery instead of copying it into the
		// legacy namespace or claiming a native publication.
		return &ToolCacheSave{Note: "native tool cache requires post-stop recovery"}
	}
	if _, err := shell(ctx, toolcacheSaveScript, s.toolcacheEnv()); err != nil {
		if ctx.Err() != nil {
			return &ToolCacheSave{Note: "timed out"}
		}
		return &ToolCacheSave{Note: err.Error()}
	}
	return &ToolCacheSave{Saved: true}
}
