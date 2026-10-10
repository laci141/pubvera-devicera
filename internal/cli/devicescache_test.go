package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// resetDevicesCache empties the shared devices cache and restores the clock.
func resetDevicesCache(t *testing.T) {
	t.Helper()
	reset := func() {
		sharedDevices.mu.Lock()
		sharedDevices.entries = make(map[string]*devicesEntry)
		sharedDevices.mu.Unlock()
	}
	reset()
	origNow := devicesNow
	t.Cleanup(func() { devicesNow = origNow; reset() })
}

// slowStub is a limitStub that holds every request for delay.
type slowStub struct {
	*limitStub
	delay time.Duration
}

func (s *slowStub) RoundTrip(r *http.Request) (*http.Response, error) {
	time.Sleep(s.delay)
	return s.limitStub.RoundTrip(r)
}

// devicesRig installs the stub upstream and a fake clock.
type devicesRig struct {
	stub *limitStub
	mu   sync.Mutex
	now  time.Time
}

func newDevicesRig(t *testing.T, status int, fail func(*http.Request) bool, delay time.Duration) *devicesRig {
	t.Helper()
	swapLUCache(t, func(context.Context) string { return "2026-09-22" }) // also resets the cache
	g := &devicesRig{now: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	g.stub = &limitStub{fail: fail, status: status}
	old := http.DefaultTransport
	http.DefaultTransport = &slowStub{limitStub: g.stub, delay: delay}
	t.Cleanup(func() { http.DefaultTransport = old })
	devicesNow = func() time.Time { g.mu.Lock(); defer g.mu.Unlock(); return g.now }
	return g
}

func (g *devicesRig) advance(d time.Duration) { g.mu.Lock(); g.now = g.now.Add(d); g.mu.Unlock() }

func (g *devicesRig) get(device string) (int, string) {
	rec := httptest.NewRecorder()
	handleDevices(rec, httptest.NewRequest(http.MethodGet, "/api/devices?device="+url.QueryEscape(device), nil))
	return rec.Code, rec.Body.String()
}

func (g *devicesRig) hits() int { return g.stub.count("/device/udi.json") }

func never(*http.Request) bool { return false }

func TestDevicesCacheSecondCallServedFromCache(t *testing.T) {
	g := newDevicesRig(t, 0, never, 0)
	c1, b1 := g.get("cache pacemaker")
	first := g.hits()
	if first == 0 {
		t.Fatal("first call made no upstream request")
	}
	c2, b2 := g.get("cache pacemaker")
	if g.hits() != first {
		t.Errorf("second identical call hit upstream: %d -> %d", first, g.hits())
	}
	if c1 != c2 || b1 != b2 {
		t.Errorf("cached response differs from uncached:\n%s\n%s", b1, b2)
	}
}

func TestDevicesCacheDifferentDeviceHitsUpstream(t *testing.T) {
	g := newDevicesRig(t, 0, never, 0)
	g.get("cache stent")
	first := g.hits()
	g.get("cache catheter")
	if g.hits() <= first {
		t.Errorf("a different device must reach upstream (hits %d -> %d)", first, g.hits())
	}
}

func TestDevicesCacheKeyNormalisation(t *testing.T) {
	g := newDevicesRig(t, 0, never, 0)
	g.get("pacemaker")
	first := g.hits()
	g.get("  Pacemaker ")
	if g.hits() != first {
		t.Errorf(`" Pacemaker " must share the "pacemaker" entry (hits %d -> %d)`, first, g.hits())
	}
}

func TestDevicesCacheCompleteUsesFullTTL(t *testing.T) {
	g := newDevicesRig(t, 0, never, 0)
	g.get("ttl full")
	h := g.hits()
	g.advance(devicesCacheTTL - time.Second)
	g.get("ttl full")
	if g.hits() != h {
		t.Errorf("complete result must still be cached just under 5m")
	}
	g.advance(2 * time.Second)
	g.get("ttl full")
	if g.hits() == h {
		t.Errorf("complete result must expire after 5m")
	}
}

func TestDevicesCacheRateLimitedUsesShortTTL(t *testing.T) {
	g := newDevicesRig(t, http.StatusTooManyRequests, failLeg("100"), 0)
	_, body := g.get("ttl limited")
	if !strings.Contains(body, `"rate_limited":true`) {
		t.Fatalf("setup: expected a rate_limited answer, got %s", body)
	}
	h := g.hits()
	g.advance(devicesShortTTL - time.Second)
	g.get("ttl limited")
	if g.hits() != h {
		t.Errorf("rate_limited result must be cached for 60s")
	}
	g.advance(2 * time.Second)
	g.get("ttl limited")
	if g.hits() == h {
		t.Errorf("rate_limited result must expire after 60s, not 5m")
	}
}

func TestDevicesCacheErrorNotCached(t *testing.T) {
	g := newDevicesRig(t, http.StatusTooManyRequests, failAll, 0)
	code, _ := g.get("err device")
	if code != http.StatusBadGateway {
		t.Fatalf("setup: status=%d want 502", code)
	}
	h := g.hits()
	g.get("err device")
	if g.hits() == h {
		t.Errorf("a failed (502) result was served from cache")
	}
}

func TestDevicesCacheConcurrentSingleUpstreamRun(t *testing.T) {
	g := newDevicesRig(t, 0, never, 150*time.Millisecond)
	const n = 8
	var wg sync.WaitGroup
	bodies := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, bodies[i] = g.get("concurrent dev") }(i)
	}
	wg.Wait()
	// One run = one brand request + one category page.
	if got := g.hits(); got != 2 {
		t.Errorf("%d concurrent identical calls made %d upstream requests, want 2 (one run)", n, got)
	}
	for i := 1; i < n; i++ {
		if bodies[i] != bodies[0] {
			t.Errorf("body %d differs from body 0", i)
		}
	}
}

func TestDevicesCacheBoundedTo32(t *testing.T) {
	g := newDevicesRig(t, 0, never, 0)
	for i := 0; i < devicesMaxItems+5; i++ {
		g.get("bound " + string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	sharedDevices.mu.Lock()
	n := len(sharedDevices.entries)
	sharedDevices.mu.Unlock()
	if n > devicesMaxItems {
		t.Errorf("cache holds %d entries, max %d", n, devicesMaxItems)
	}
}
