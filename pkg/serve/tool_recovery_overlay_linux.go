//go:build linux

package serve

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nektos/act/pkg/artifactcache"
	"golang.org/x/sys/unix"
)

const recoveryMountInfoLimit = 1 << 20

type toolRecoveryOverlayPaths struct{ lower, upper, work string }

func openToolRecoveryOverlay(ctx context.Context, request ToolRecoveryOverlayRequest) (artifactcache.ToolRecoverySourceView, error) {
	if err := ctx.Err(); err != nil {
		return artifactcache.ToolRecoverySourceView{}, err
	}
	paths, err := recoveryOverlayPaths(request)
	if err != nil {
		return artifactcache.ToolRecoverySourceView{}, err
	}
	target, err := os.MkdirTemp("/tmp", "act-tool-recovery-")
	if err != nil {
		return artifactcache.ToolRecoverySourceView{}, err
	}
	mounted := false
	closeView := func() error {
		if mounted {
			if err := unix.Unmount(target, 0); err != nil {
				return fmt.Errorf("unmount recovery view %s: %w", target, err)
			}
			mounted = false
		}
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove recovery view %s: %w", target, err)
		}
		return nil
	}
	options := "metacopy=on,lowerdir=" + paths.lower + ",upperdir=" + paths.upper + ",workdir=" + paths.work
	if err := unix.Mount("overlay", target, "overlay", unix.MS_RDONLY, options); err != nil {
		return artifactcache.ToolRecoverySourceView{Path: target, Close: closeView}, fmt.Errorf("mount recovery overlay: %w", err)
	}
	mounted = true
	err = verifyRecoveryOverlayMount(target, paths)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return artifactcache.ToolRecoverySourceView{Path: target, Close: closeView}, err
	}
	return artifactcache.ToolRecoverySourceView{Path: target, Close: closeView}, nil
}

func recoveryOverlayPaths(request ToolRecoveryOverlayRequest) (toolRecoveryOverlayPaths, error) {
	paths := toolRecoveryOverlayPaths{lower: filepath.Join(request.Generation.Store, ".tool-generations-v1", request.Pin.Generation, "tree"), upper: filepath.Join(request.SourceRoot, nativeToolUpperName), work: filepath.Join(request.SourceRoot, nativeToolWorkName)}
	for _, path := range []string{request.SourceRoot, paths.lower, paths.upper, paths.work} {
		if len(path) > 4096 || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, ",:\\ \t\r\n\x00") {
			return paths, fmt.Errorf("recovery overlay path is not canonical or mount-safe")
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			return paths, fmt.Errorf("recovery overlay path is missing or aliased")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			return paths, fmt.Errorf("recovery overlay path is not a directory")
		}
	}
	if recoveryPathsOverlap(request.SourceRoot, request.Generation.Store) {
		return paths, fmt.Errorf("recovery source and store overlap")
	}
	return paths, nil
}

func recoveryPathsOverlap(a, b string) bool {
	return a == b || a == string(filepath.Separator) || b == string(filepath.Separator) || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

func verifyRecoveryOverlayMount(target string, paths toolRecoveryOverlayPaths) error {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, recoveryMountInfoLimit+1))
	if err != nil {
		return err
	}
	if len(data) > recoveryMountInfoLimit {
		return fmt.Errorf("recovery mount inventory exceeds bound")
	}
	return requireRecoveryOverlayMount(string(data), target, paths)
}

func requireRecoveryOverlayMount(document, target string, paths toolRecoveryOverlayPaths) error {
	count := 0
	valid := false
	for _, line := range strings.Split(document, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 || fields[4] != target {
			continue
		}
		count++
		separator := 0
		for index := 6; index < len(fields); index++ {
			if fields[index] == "-" {
				separator = index
				break
			}
		}
		if separator == 0 || len(fields) <= separator+3 || fields[separator+1] != "overlay" || fields[separator+2] != "overlay" {
			continue
		}
		vfs, vfsErr := recoveryMountOptions(fields[5])
		super, superErr := recoveryMountOptions(fields[separator+3])
		if vfsErr != nil || superErr != nil {
			continue
		}
		valid = recoveryMountReadOnly(vfs) && recoveryMountReadOnly(super) && super["metacopy"] == "on" && super["lowerdir"] == paths.lower && super["upperdir"] == paths.upper && super["workdir"] == paths.work
	}
	if count != 1 || !valid {
		return fmt.Errorf("recovery overlay does not prove the exact read-only metacopy binding")
	}
	return nil
}

func recoveryMountOptions(text string) (map[string]string, error) {
	options := make(map[string]string)
	for _, part := range strings.Split(text, ",") {
		key, value, _ := strings.Cut(part, "=")
		if key == "" {
			return nil, fmt.Errorf("empty recovery mount option")
		}
		if _, exists := options[key]; exists {
			return nil, fmt.Errorf("duplicate recovery mount option")
		}
		options[key] = value
	}
	return options, nil
}

func recoveryMountReadOnly(options map[string]string) bool {
	value, ro := options["ro"]
	_, rw := options["rw"]
	return ro && value == "" && !rw
}
