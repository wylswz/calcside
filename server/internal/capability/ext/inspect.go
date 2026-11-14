package ext

import "sync"

// Inspector reads extension metadata without initializing modules. The
// API tier uses it to find the llm capability extension before the node
// validates the spec, instead of inferring behavior from source names.
type Inspector struct {
	opts  *Options
	locks *fetchLocks
	mu    sync.Mutex
	kinds map[string]string
}

func NewInspector(opts *Options) *Inspector {
	return &Inspector{opts: opts, locks: &fetchLocks{}, kinds: map[string]string{}}
}

// Capability returns the manifest's capability marker (empty for normal
// extensions). Results are cached per source/sum for this process.
func (i *Inspector) Capability(source, sum string) (string, error) {
	key := source + "\x00" + sum
	i.mu.Lock()
	if kind, ok := i.kinds[key]; ok {
		i.mu.Unlock()
		return kind, nil
	}
	i.mu.Unlock()

	f := factory{opts: i.opts, locks: i.locks}
	mod, err := f.loadModule(source, sum)
	if err != nil {
		return "", err
	}
	kind := mod.Manifest.Capability
	i.mu.Lock()
	i.kinds[key] = kind
	i.mu.Unlock()
	return kind, nil
}
