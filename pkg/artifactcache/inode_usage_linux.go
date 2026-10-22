//go:build linux

package artifactcache

import (
	"fmt"
	"math"
)

type usageInode struct{ device, inode uint64 }

type inodeUsageCounter struct {
	usage                                  InodeUsage
	seen                                   map[usageInode]struct{}
	apparent, allocated, files, references int64
}

func (c *inodeUsageCounter) add(device, inode uint64, size, blocks int64, apparent, regular bool) error {
	if blocks < 0 || blocks > math.MaxInt64/512 {
		return fmt.Errorf("inode allocation is invalid or oversized")
	}
	if c.usage.FilesystemDevice == nil {
		c.usage.FilesystemDevice = &device
	} else if *c.usage.FilesystemDevice != device {
		return fmt.Errorf("inode inventory crosses a filesystem boundary")
	}
	c.usage.PathEntries++
	if regular {
		if err := addToolUsageBytes(&c.references, size); err != nil {
			return err
		}
	}
	key := usageInode{device, inode}
	if _, exists := c.seen[key]; exists {
		return nil
	}
	if c.seen == nil {
		c.seen = make(map[usageInode]struct{})
	}
	c.seen[key] = struct{}{}
	c.usage.UniqueInodes++
	if apparent {
		if err := addToolUsageBytes(&c.apparent, size); err != nil {
			return err
		}
	}
	if err := addToolUsageBytes(&c.allocated, blocks*512); err != nil {
		return err
	}
	if regular {
		return addToolUsageBytes(&c.files, size)
	}
	return nil
}

func (c *inodeUsageCounter) report(complete bool) InodeUsage {
	result := c.usage
	if complete {
		apparent, allocated, files, references := c.apparent, c.allocated, c.files, c.references
		result.ApparentBytes, result.AllocatedBytes = &apparent, &allocated
		result.UniqueFileBytes, result.ReferencedFileBytes = &files, &references
	}
	return result
}

func addToolUsageBytes(total *int64, size int64) error {
	if size < 0 || size > math.MaxInt64-*total {
		return fmt.Errorf("tool store byte total is invalid or oversized")
	}
	*total += size
	return nil
}
