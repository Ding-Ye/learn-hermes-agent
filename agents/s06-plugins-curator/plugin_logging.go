package main

import (
	"context"
	"fmt"
	"time"
)

// LoggingPlugin is the smallest possible plugin. Its only job is to print
// a line on every lifecycle event. Useful as a sanity check and as the
// shape every other plugin starts from.
type LoggingPlugin struct {
	host *Host
}

func NewLoggingPlugin() *LoggingPlugin { return &LoggingPlugin{} }

func (p *LoggingPlugin) Name() string { return "logging" }

func (p *LoggingPlugin) Init(ctx context.Context, host *Host) error {
	p.host = host
	host.Logger.Infof("[plugin:logging] init at %s", time.Now().Format(time.RFC3339))
	return nil
}

func (p *LoggingPlugin) OnSessionStart(ctx context.Context, sid string) error {
	p.host.Logger.Infof("[plugin:logging] session start %s", sid)
	return nil
}

func (p *LoggingPlugin) OnSessionEnd(ctx context.Context, sid string) error {
	p.host.Logger.Infof("[plugin:logging] session end %s", sid)
	return nil
}

func (p *LoggingPlugin) Close() error {
	if p.host != nil {
		p.host.Logger.Infof("[plugin:logging] close")
	}
	return nil
}

// AssertHostNotNil is a tiny convenience the tests use.
func (p *LoggingPlugin) AssertHostNotNil() error {
	if p.host == nil {
		return fmt.Errorf("Init was not called")
	}
	return nil
}
