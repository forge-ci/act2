//go:build linux

package artifactcache

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var footprintClasses = [...]string{"tools", "images", "actions", "toolcache", "actcache", "other"}

type footprintWalk struct {
	ctx            context.Context
	mount          uint64
	limit          int
	total          inodeUsageCounter
	classes        [len(footprintClasses)]inodeUsageCounter
	namespaces     namespaceUsageHeap
	namespaceCount int
}

func auditCacheFootprint(ctx context.Context, root string, maxEntries int) CacheFootprint {
	report := CacheFootprint{SchemaVersion: 1, Root: root, ObservedAt: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	walk := footprintWalk{ctx: ctx, limit: maxEntries}
	err := walk.scan(root)
	complete := err == nil
	report.Total = walk.total.report(complete)
	for i, class := range footprintClasses {
		report.Components = append(report.Components, CacheUsageComponent{Class: class, Usage: walk.classes[i].report(complete)})
	}
	if complete {
		report.Namespaces = walk.namespaceReport()
		report.OmittedNamespaces = walk.namespaceCount - len(report.Namespaces)
	}
	report.Partial = !complete
	if err != nil {
		report.Error = toolReportError(err)
	}
	report.ObservedAt = time.Now().UTC()
	return report
}

func (w *footprintWalk) scan(root string) error {
	if w.limit <= 0 || w.limit > 1000000 {
		return fmt.Errorf("cache inventory requires an entry bound of 1..1000000")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return fmt.Errorf("cache inventory requires a clean absolute non-filesystem-root directory")
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}
	directory, err := openFootprintRoot(w.ctx, root)
	if err != nil {
		return err
	}
	defer directory.Close()
	fd := int(directory.Fd())
	info, err := footprintStat(fd, "", unix.AT_EMPTY_PATH)
	if err != nil {
		return err
	}
	w.mount = info.Mnt_id
	if err := w.add(info, -1, nil); err != nil {
		return err
	}
	return w.directory(directory, -1, 0, "", nil)
}

// Directory descriptors, no-follow opens and statx mount identities keep a
// concurrent rename/symlink substitution from escaping the caller's root.
func (w *footprintWalk) directory(directory *os.File, class, depth int, relative string, namespace *inodeUsageCounter) error {
	if depth >= 72 {
		return fmt.Errorf("cache inventory depth bound exceeded")
	}
	for {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		names, readErr := directory.Readdirnames(256)
		for _, name := range names {
			if err := w.ctx.Err(); err != nil {
				return err
			}
			childClass := class
			if depth == 0 {
				childClass = footprintClass(name)
			}
			if err := w.entry(int(directory.Fd()), name, childClass, depth, filepath.Join(relative, name), namespace); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (w *footprintWalk) entry(parent int, name string, class, depth int, relative string, namespace *inodeUsageCounter) error {
	info, err := footprintStat(parent, name, unix.AT_SYMLINK_NOFOLLOW)
	if err != nil {
		return err
	}
	namespaceName := ""
	if info.Mode&unix.S_IFMT == unix.S_IFDIR && namespace == nil {
		namespaceName = footprintNamespace(relative)
		if namespaceName != "" {
			namespace = &inodeUsageCounter{}
		}
	}
	if err := w.add(info, class, namespace); err != nil {
		return err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	child := os.NewFile(uintptr(fd), name)
	defer child.Close()
	opened, err := footprintStat(fd, "", unix.AT_EMPTY_PATH)
	if err != nil {
		return err
	}
	if opened.Ino != info.Ino || opened.Dev_major != info.Dev_major || opened.Dev_minor != info.Dev_minor || opened.Mnt_id != info.Mnt_id {
		return fmt.Errorf("cache directory changed during inventory")
	}
	if err := w.directory(child, class, depth+1, relative, namespace); err != nil {
		return err
	}
	if namespaceName != "" {
		w.rememberNamespace(relative, namespaceName, namespace)
	}
	return nil
}

func footprintStat(fd int, path string, flags int) (unix.Statx_t, error) {
	var stat unix.Statx_t
	required := uint32(unix.STATX_TYPE | unix.STATX_INO | unix.STATX_SIZE | unix.STATX_BLOCKS | unix.STATX_MNT_ID)
	err := unix.Statx(fd, path, flags|unix.AT_NO_AUTOMOUNT, int(required), &stat)
	if err != nil {
		return stat, err
	}
	if stat.Mask&required != required {
		return stat, fmt.Errorf("cache inode or mount metadata unavailable")
	}
	return stat, nil
}

func (w *footprintWalk) add(stat unix.Statx_t, class int, namespace *inodeUsageCounter) error {
	if w.total.usage.PathEntries >= w.limit {
		return fmt.Errorf("cache inventory entry bound exceeded")
	}
	if stat.Mnt_id != w.mount {
		return fmt.Errorf("cache inventory crosses a mount boundary")
	}
	if stat.Size > math.MaxInt64 || stat.Blocks > math.MaxInt64 {
		return fmt.Errorf("cache inode metadata is oversized")
	}
	mode := stat.Mode & unix.S_IFMT
	apparent := mode == unix.S_IFDIR || mode == unix.S_IFREG || mode == unix.S_IFLNK
	device := unix.Mkdev(stat.Dev_major, stat.Dev_minor)
	if err := w.total.add(device, stat.Ino, int64(stat.Size), int64(stat.Blocks), apparent, mode == unix.S_IFREG); err != nil {
		return err
	}
	if class >= 0 {
		if err := w.classes[class].add(device, stat.Ino, int64(stat.Size), int64(stat.Blocks), apparent, mode == unix.S_IFREG); err != nil {
			return err
		}
	}
	if namespace != nil {
		return namespace.add(device, stat.Ino, int64(stat.Size), int64(stat.Blocks), apparent, mode == unix.S_IFREG)
	}
	return nil
}

func footprintClass(name string) int {
	for i, class := range footprintClasses[:len(footprintClasses)-1] {
		if name == class {
			return i
		}
	}
	if name == "toolstore-v1" || name == ".toolstore-v1.enrollment-v1.bolt" || strings.HasPrefix(name, ".toolcache.native-enrollment-") {
		return 3
	}
	return len(footprintClasses) - 1
}

// Open every component relative to the preceding descriptor: O_NOFOLLOW on
// only the final path would still follow aliased ancestors and trailing '/.'.
func openFootprintRoot(ctx context.Context, path string) (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, name := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if err := ctx.Err(); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}
