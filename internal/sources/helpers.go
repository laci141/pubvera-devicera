package sources

import (
	"encoding/json"
	"net/url"
	"strconv"
)

// paramsForSearch builds the common openFDA pagination params. The "search"
// value is set by the caller AFTER this, so url.Values.Encode handles the
// space-to-"+" encoding of the Lucene expression uniformly.
func paramsForSearch(q Query) url.Values {
	v := url.Values{}
	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	v.Set("limit", strconv.Itoa(limit))
	if q.Skip > 0 {
		v.Set("skip", strconv.Itoa(q.Skip))
	}
	// Without sort, openFDA returns index order, so a page's max date is not
	// the newest record (verified live 2026-09-29: sort=<date field>:desc → 200).
	if q.Sort != "" {
		v.Set("sort", q.Sort)
	}
	return v
}

// parseCounts decodes an openFDA count=... response ({results:[{term,count}]})
// into a term->count map. Shared by every count capability. A count on a date
// field (e.g. count=date_received) answers {time,count} daily buckets instead;
// those are keyed by their YYYYMMDD time.
func parseCounts(body []byte) (map[string]int, error) {
	var env struct {
		Results []struct {
			Term  string `json:"term"`
			Time  string `json:"time"`
			Count int    `json:"count"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(env.Results))
	for _, r := range env.Results {
		key := r.Term
		if key == "" {
			key = r.Time
		}
		out[key] = r.Count
	}
	return out, nil
}
