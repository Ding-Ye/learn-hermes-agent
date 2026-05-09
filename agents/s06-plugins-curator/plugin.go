package main

import (
	"context"
	"fmt"
	"sync"
)

// Plugin is the contract every extension implements. The interface is
// intentionally small — five lifecycle moments — because hermes's design
// is "many small plugins composed", not "few mega-plugins". A plugin
// that only logs subscribes to nothing dynamic; the Curator subscribes
// to OnSessionStart to do its periodic work.
type Plugin interface {
	Name() string

	// Init runs once before the agent's first turn. The plugin may
	// register tools on host.Registry, schedule goroutines, open
	// connections, etc.
	Init(ctx context.Context, host *Host) error

	// OnSessionStart fires when the loop is about to handle a fresh
	// (or resumed) session.
	OnSessionStart(ctx context.Context, sessionID string) error

	// OnSessionEnd fires after Run returns or errors. Best-effort.
	OnSessionEnd(ctx context.Context, sessionID string) error

	// Close releases resources. Always invoked from the manager's Close,
	// even if Init never succeeded.
	Close() error
}

// Host is the surface plugins are allowed to touch during Init. Keeping it
// to a struct (not the whole agent) makes future capability auditing
// easier and prevents plugins from reaching into private agent guts.
type Host struct {
	Registry *Registry
	Memory   MemoryProvider
	Logger   *Logger
}

// PluginManager owns a list of plugins and fans lifecycle events through
// them. Errors from individual plugins are *aggregated*, not propagated:
// a single misbehaving plugin must not take down the agent.
type PluginManager struct {
	mu      sync.Mutex
	plugins []Plugin
	logger  *Logger
}

func NewPluginManager(logger *Logger) *PluginManager {
	return &PluginManager{logger: logger}
}

// Register appends a plugin. Init is NOT called here — the manager calls
// it later via Init(ctx, host) so all plugins share one host.
func (pm *PluginManager) Register(p Plugin) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.plugins = append(pm.plugins, p)
}

func (pm *PluginManager) Names() []string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	out := make([]string, 0, len(pm.plugins))
	for _, p := range pm.plugins {
		out = append(out, p.Name())
	}
	return out
}

func (pm *PluginManager) Init(ctx context.Context, host *Host) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	var errs []error
	for _, p := range pm.plugins {
		if err := p.Init(ctx, host); err != nil {
			pm.logger.Errorf("plugin %s Init: %v", p.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
		}
	}
	return joinErrors(errs)
}

func (pm *PluginManager) DispatchSessionStart(ctx context.Context, sid string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, p := range pm.plugins {
		if err := p.OnSessionStart(ctx, sid); err != nil {
			pm.logger.Errorf("plugin %s OnSessionStart: %v", p.Name(), err)
		}
	}
}

func (pm *PluginManager) DispatchSessionEnd(ctx context.Context, sid string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, p := range pm.plugins {
		if err := p.OnSessionEnd(ctx, sid); err != nil {
			pm.logger.Errorf("plugin %s OnSessionEnd: %v", p.Name(), err)
		}
	}
}

func (pm *PluginManager) Close() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, p := range pm.plugins {
		if err := p.Close(); err != nil {
			pm.logger.Errorf("plugin %s Close: %v", p.Name(), err)
		}
	}
}

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	msg := fmt.Sprintf("%d plugin errors:", len(errs))
	for _, e := range errs {
		msg += "\n  - " + e.Error()
	}
	return fmt.Errorf("%s", msg)
}

// Logger is a thin facade so plugins don't import log directly. Lets us
// route everything through one prefix in main.go.
type Logger struct {
	Prefix string
	Out    func(string)
}

func (l *Logger) Infof(format string, args ...interface{}) {
	if l == nil || l.Out == nil {
		return
	}
	l.Out(fmt.Sprintf(l.Prefix+" "+format, args...))
}

func (l *Logger) Errorf(format string, args ...interface{}) {
	if l == nil || l.Out == nil {
		return
	}
	l.Out(fmt.Sprintf(l.Prefix+" ERROR: "+format, args...))
}
