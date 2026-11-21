// wiring_preview.go holds the previewer behind POST /api/preview: it
// builds the draft config (running config + draft channels, a preset or
// assist switches) and hands it to the controller's Preview.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// previewCtrl is the controller side (*control.Controller; a fake in tests).
type previewCtrl interface {
	Config() config.Config
	Preview(draft config.Config, span time.Duration) (control.PreviewResult, error)
}

type previewer struct {
	ctrl    previewCtrl
	presets dirPresetStore
}

func (p previewer) RunningChannels() []config.Channel { return p.ctrl.Config().Channels }

// Preview builds the draft from the running config:
//   - preset: its channels merged by pwm like an apply (mergeChannelsByPWM:
//     the running names, post-processing and ceiling stay where the preset
//     says nothing); channels the daemon does not run are left out;
//   - channels: each replaces the running channel of its name, the running
//     ceiling stays (the editor has no ceiling field);
//   - assist: the switch per channel; a channel without a running
//     [[assist]] table is web.ErrBadInput.
func (p previewer) Preview(req web.PreviewRequest) (any, error) {
	cfg := p.ctrl.Config()
	idx := map[string]int{}
	for i, c := range cfg.Channels {
		idx[c.Name] = i
	}
	if req.Preset != "" {
		chans, _, err := p.presets.load(req.Preset)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("unknown preset %q: %w", req.Preset, fs.ErrNotExist)
		case err != nil:
			return nil, fmt.Errorf("preset %q: %v: %w", req.Preset, err, web.ErrBadInput)
		case len(chans) == 0:
			return nil, fmt.Errorf("preset %q has no usable [[channel]] table: %w", req.Preset, web.ErrBadInput)
		}
		var keep []config.Channel
		for _, c := range mergeChannelsByPWM(cfg.Channels, chans) {
			if _, running := idx[c.Name]; running {
				keep = append(keep, c)
			}
		}
		cfg.Channels = keep
	}
	for _, d := range req.Channels {
		i, ok := idx[d.Name]
		if !ok {
			return nil, fmt.Errorf("channel %q is not running: %w", d.Name, web.ErrBadInput)
		}
		if d.Ceiling == 0 {
			d.Ceiling = cfg.Channels[i].Ceiling
		}
		cfg.Channels[i] = d
	}
	for name, on := range req.Assist {
		a := cfg.Assist(name)
		if a == nil {
			return nil, fmt.Errorf("channel %q has no [[assist]] table: %w", name, web.ErrBadInput)
		}
		a.Enabled = on
	}
	return p.ctrl.Preview(cfg, req.Span)
}
