//go:build linux

package artifactcache

import (
	"container/heap"
	"sort"
	"strings"
)

const footprintNamespaceLimit = 256

type namespaceUsageHeap []CacheUsageNamespace

// The least useful detail is at the root; keep the largest allocations with
// deterministic path ordering. These details never contribute to Total.
func (h namespaceUsageHeap) Len() int { return len(h) }
func (h namespaceUsageHeap) Less(i, j int) bool {
	a, b := h[i], h[j]
	if *a.Usage.AllocatedBytes != *b.Usage.AllocatedBytes {
		return *a.Usage.AllocatedBytes < *b.Usage.AllocatedBytes
	}
	return a.StorePath > b.StorePath
}
func (h namespaceUsageHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *namespaceUsageHeap) Push(value any) { *h = append(*h, value.(CacheUsageNamespace)) }
func (h *namespaceUsageHeap) Pop() any {
	old := *h
	value := old[len(old)-1]
	*h = old[:len(old)-1]
	return value
}

func footprintNamespace(path string) string {
	parts := strings.Split(path, "/")
	validParent := len(parts) == 2 && parts[0] == "actcache" || len(parts) == 3 && parts[0] == "actcache" && parts[1] == "cohort-v1"
	if !validParent {
		return ""
	}
	name := parts[len(parts)-1]
	if len(name) != 16 {
		return ""
	}
	for _, ch := range name {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return ""
		}
	}
	return name
}

func (w *footprintWalk) rememberNamespace(path, name string, counter *inodeUsageCounter) {
	w.namespaceCount++
	heap.Push(&w.namespaces, CacheUsageNamespace{Namespace: name, StorePath: path, Usage: counter.report(true)})
	if len(w.namespaces) > footprintNamespaceLimit {
		heap.Pop(&w.namespaces)
	}
}

func (w *footprintWalk) namespaceReport() []CacheUsageNamespace {
	result := []CacheUsageNamespace(w.namespaces)
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if *a.Usage.AllocatedBytes != *b.Usage.AllocatedBytes {
			return *a.Usage.AllocatedBytes > *b.Usage.AllocatedBytes
		}
		return a.StorePath < b.StorePath
	})
	return result
}
