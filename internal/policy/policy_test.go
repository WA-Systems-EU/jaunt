package policy

import (
	"context"
	"reflect"
	"testing"

	"github.com/WA-Systems-EU/jaunt/internal/config"
)

func TestLocalSourceReturnsConfigLimits(t *testing.T) {
	cfg := config.Default()
	var src Source = Local{Cfg: cfg}
	p, err := src.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Limits != cfg.Limits {
		t.Errorf("limits = %+v, want %+v", p.Limits, cfg.Limits)
	}
	if !reflect.DeepEqual(p.Profiles, cfg.Profiles) {
		t.Errorf("profiles = %+v, want %+v", p.Profiles, cfg.Profiles)
	}
}
