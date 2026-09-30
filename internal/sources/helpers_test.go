package sources

import "testing"

// TestParamsForSearchSort: a requested sort reaches openFDA as sort=<field>:<dir>,
// and an unsorted query sends no sort key at all (openFDA's default order).
func TestParamsForSearchSort(t *testing.T) {
	v := paramsForSearch(Query{Limit: 25, Sort: "recall_initiation_date:desc"})
	if got := v.Get("sort"); got != "recall_initiation_date:desc" {
		t.Errorf("sort=%q want recall_initiation_date:desc", got)
	}
	if got := v.Get("limit"); got != "25" {
		t.Errorf("limit=%q want 25", got)
	}
	if v := paramsForSearch(Query{Limit: 25}); v.Has("sort") {
		t.Errorf("unsorted query must not send sort, got %q", v.Get("sort"))
	}
}

// TestParseCountsTimeBuckets: a count on a date field (count=date_received)
// answers {time,count} buckets, not {term,count}; each day must keep its key
// (measured 2026-09-30: pacemaker → 8664 daily buckets summing to the total).
func TestParseCountsTimeBuckets(t *testing.T) {
	body := []byte(`{"results":[{"time":"20260830","count":7},{"time":"20260831","count":5}]}`)
	got, err := parseCounts(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["20260830"] != 7 || got["20260831"] != 5 {
		t.Errorf("got %v want map[20260830:7 20260831:5]", got)
	}
	terms, _ := parseCounts([]byte(`{"results":[{"term":"Injury","count":3}]}`))
	if terms["Injury"] != 3 {
		t.Errorf("term buckets broke: %v", terms)
	}
}
