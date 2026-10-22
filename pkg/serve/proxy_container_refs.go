package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// scopedContainer resolves only an existing container owned by this run.
// IDs and already-qualified names retain their Docker meaning. Unresolved
// names still pass through the ordinary ownership check before forwarding.
func (p *runProxy) scopedContainer(ctx context.Context, name string) (string, error) {
	if name == "" || strings.HasSuffix(name, "-"+p.scope.Key) {
		return name, nil
	}
	candidate := name + "-" + p.scope.Key
	owner, _, err := p.inspect(ctx, kindContainer, candidate)
	if err != nil {
		return "", err
	}
	if !owner.missing && owner.run == p.scope.RunID {
		return candidate, nil
	}
	return name, nil
}

// rewriteContainerReferences keeps Docker's aliases and access modes while
// resolving the names that were qualified by this proxy on container create.
func (p *runProxy) rewriteContainerReferences(ctx context.Context, body []byte) ([]byte, error) {
	object, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	host, err := child(object, "HostConfig")
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"NetworkMode", "PidMode", "IpcMode"} {
		if mode, ok := host[key].(string); ok && strings.HasPrefix(mode, "container:") {
			name, err := p.scopedContainer(ctx, strings.TrimPrefix(mode, "container:"))
			if err != nil {
				return nil, err
			}
			host[key] = "container:" + name
		}
	}
	for _, key := range []string{"VolumesFrom", "Links"} {
		entries, ok := host[key].([]any)
		if !ok {
			continue
		}
		for i, entry := range entries {
			value, ok := entry.(string)
			if !ok {
				return nil, fmt.Errorf("%s contains a non-string reference", key)
			}
			name, suffix, colon := strings.Cut(value, ":")
			slash := strings.HasPrefix(name, "/")
			mapped, err := p.scopedContainer(ctx, strings.TrimPrefix(name, "/"))
			if err != nil {
				return nil, err
			}
			if slash {
				mapped = "/" + mapped
			}
			if colon {
				mapped += ":" + suffix
			}
			entries[i] = mapped
		}
	}
	return json.Marshal(object)
}
