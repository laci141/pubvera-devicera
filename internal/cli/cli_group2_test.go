package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/laci141/medical-device-intelligence/internal/sources"
)

// fakeEventSource is a fakeSource that also implements sources.EventCounter.
type fakeEventSource struct {
	fakeSource
	counts map[string]int
}

func (f fakeEventSource) CountEventTypes(context.Context, sources.Query) (map[string]int, error) {
	return f.counts, nil
}

func eventRec(id, etype, date string, problems ...any) sources.RawRecord {
	return sources.RawRecord{ID: id, Raw: map[string]any{
		"event_type": etype, "date_received": date, "product_problems": problems,
	}}
}

func TestAdverseBreakdownAndCitation(t *testing.T) {
	withSources(t, map[string]sources.Source{
		"openfda_device_event": fakeEventSource{
			fakeSource: fakeSource{name: "openfda_device_event", id: "mdr_report_key",
				recs: []sources.RawRecord{eventRec("M-1", "Injury", "20240615", "Under-Sensing")}},
			counts: map[string]int{"Death": 5, "Injury": 20, "Malfunction": 100},
		},
	})
	out, _, code := run(cmdAdverse, "pacemaker")
	if code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}
	if !strings.Contains(out, "serious_events") || !strings.Contains(out, "25") { // 5+20
		t.Errorf("expected serious_events breakdown of 25, got:\n%s", out)
	}
	if !strings.Contains(out, "deaths") || !strings.Contains(out, "M-1") {
		t.Error("must show deaths count and cite mdr_report_key")
	}
	if !strings.Contains(out, "not medical advice") {
		t.Error("adverse must carry the disclaimer")
	}
}

func TestAdverseBadSeverityExit2(t *testing.T) {
	withSources(t, map[string]sources.Source{
		"openfda_device_event": fakeEventSource{fakeSource: fakeSource{name: "openfda_device_event", id: "mdr_report_key"}},
	})
	_, errStr, code := run(cmdAdverse, "pacemaker", "--severity", "nope")
	if code != 2 {
		t.Fatalf("bad severity exit=%d want 2", code)
	}
	if !strings.Contains(errStr, "serious") {
		t.Errorf("expected severity validation message, got %q", errStr)
	}
}

func TestAdverseNoMatch(t *testing.T) {
	withSources(t, map[string]sources.Source{
		"openfda_device_event": fakeEventSource{
			fakeSource: fakeSource{name: "openfda_device_event", id: "mdr_report_key"},
			counts:     map[string]int{},
		},
	})
	out, _, code := run(cmdAdverse, "zzznope")
	if code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}
	low := strings.ToLower(out)
	if !strings.Contains(low, "no adverse events found") {
		t.Errorf("want 'no adverse events found', got:\n%s", out)
	}
	if strings.Contains(low, "is safe") || strings.Contains(low, "are safe") {
		t.Error("must never affirm safety")
	}
}

func TestSafetyComposite(t *testing.T) {
	withSources(t, map[string]sources.Source{
		"openfda_device_enforcement": fakeSource{name: "openfda_device_enforcement", id: "recall_number",
			recs: []sources.RawRecord{enforcementRec()}},
		"openfda_device_event": fakeEventSource{
			fakeSource: fakeSource{name: "openfda_device_event", id: "mdr_report_key",
				recs: []sources.RawRecord{eventRec("M-9", "Death", "20240701")}},
			counts: map[string]int{"Death": 3, "Injury": 7},
		},
	})
	out, _, code := run(cmdSafety, "pacemaker")
	if code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}
	for _, want := range []string{"recalls_total", "serious_adverse_events", "deaths", "regulatory_coverage", "Z-1-2024", "M-9"} {
		if !strings.Contains(out, want) {
			t.Errorf("safety output missing %q\n%s", want, out)
		}
	}
	if !strings.Contains(out, "not a safety verdict") {
		t.Error("safety must carry the disclaimer")
	}
}

// TestTimelineSortedNewestFirst: in plain and CSV output a newer MAUDE month
// (2024-06) sorts before an older recall (2020). MAUDE is monthly counts now,
// not individual reports; the full merge order is TestTimelineMergedOrderAndLagNote.
func TestTimelineSortedNewestFirst(t *testing.T) {
	withTimelineSources(t, []sources.RawRecord{recallRec("Z-OLD", "20200101")}, nil, map[string]int{"20240615": 3}, nil)
	out, _, code := run(cmdTimeline, "--months", "1", "pacemaker")
	if code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}
	iNew := strings.Index(out, "2024-06")
	iOld := strings.Index(out, "Z-OLD")
	if iNew < 0 || iOld < 0 {
		t.Fatalf("both events must appear; got:\n%s", out)
	}
	if iNew > iOld {
		t.Errorf("newer month (2024) must sort before older recall (2020); got order new@%d old@%d", iNew, iOld)
	}
	csvOut, _, code := run(cmdTimeline, "--csv", "--months", "1", "pacemaker")
	if code != 0 || !strings.HasPrefix(csvOut, "count,date,description,event_type,kind,month,note,source,source_id\n") {
		t.Errorf("csv exit=%d header:\n%s", code, csvOut)
	}
}

func TestGroup2CommandsRegistered(t *testing.T) {
	for _, name := range []string{"adverse", "safety", "timeline"} {
		if _, ok := commands[name]; !ok {
			t.Errorf("command %q not registered", name)
		}
	}
}
