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
