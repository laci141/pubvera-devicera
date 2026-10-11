package cli

import (
	"context"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"
)

type prewarmRec struct {
	mu   sync.Mutex
	list []string
}

func (p *prewarmRec) add(d string) { p.mu.Lock(); p.list = append(p.list, d); p.mu.Unlock() }
func (p *prewarmRec) get() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.list...)
}

// swapDevicesPrewarm replaces the per-device warmer, the gap and the env.
func swapDevicesPrewarm(t *testing.T, env string, rec *prewarmRec) {
	t.Helper()
	oldFn, oldGap, oldEvery, oldGet, oldOn := prewarmDevice, prewarmDeviceGap, prewarmDevicesEvery, getenv, prewarmEnabled
	prewarmDevice = func(_ context.Context, d string) int { rec.add(d); return 200 }
	prewarmDeviceGap = time.Millisecond
	prewarmDevicesEvery = time.Hour
	prewarmEnabled = true
	getenv = func(k string) string {
		if k == "DEVICERA_PREWARM_DEVICES" {
			return env
		}
		return ""
	}
	t.Cleanup(func() {
		prewarmDevice, prewarmDeviceGap, prewarmDevicesEvery, getenv, prewarmEnabled = oldFn, oldGap, oldEvery, oldGet, oldOn
	})
}

func TestDevicesPrewarmReadsEnvList(t *testing.T) {
	rec := &prewarmRec{}
	swapDevicesPrewarm(t, " alpha ,beta,, gamma delta ", rec)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDevicesPrewarm(ctx, io.Discard)
	want := []string{"alpha", "beta", "gamma delta"}
	waitFor(t, "env list warmed", func() bool { return len(rec.get()) >= 3 })
	if got := rec.get()[:3]; !reflect.DeepEqual(got, want) {
		t.Errorf("warmed %v want %v", got, want)
	}
}

func TestDevicesPrewarmDefaultList(t *testing.T) {
	want := []string{"pacemaker", "stent", "defibrillator", "catheter", "insulin pump"}
	if got := prewarmDeviceList(func(string) string { return "" }); !reflect.DeepEqual(got, want) {
		t.Errorf("default list %v want %v", got, want)
	}
}

func TestDevicesPrewarmStopsOnCancel(t *testing.T) {
	rec := &prewarmRec{}
	swapDevicesPrewarm(t, "a,b,c,d", rec)
	prewarmDeviceGap = 10 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	startDevicesPrewarm(ctx, io.Discard)
	waitFor(t, "first device", func() bool { return len(rec.get()) == 1 })
	cancel()
	time.Sleep(150 * time.Millisecond)
	if n := len(rec.get()); n != 1 {
		t.Errorf("warmed %d devices after cancel, want 1", n)
	}
}

func TestDevicesPrewarmRepeats(t *testing.T) {
	rec := &prewarmRec{}
	swapDevicesPrewarm(t, "a", rec)
	prewarmDevicesEvery = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDevicesPrewarm(ctx, io.Discard)
	waitFor(t, "second round", func() bool { return len(rec.get()) >= 2 })
}

func TestDevicesPrewarmDisabled(t *testing.T) {
	rec := &prewarmRec{}
	swapDevicesPrewarm(t, "a", rec)
	prewarmEnabled = false
	startDevicesPrewarm(context.Background(), io.Discard)
	time.Sleep(80 * time.Millisecond)
	if n := len(rec.get()); n != 0 {
		t.Errorf("warmed %d with prewarm disabled", n)
	}
}
