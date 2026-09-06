package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/laci141/medical-device-intelligence/internal/cliutil"
)

// PMIDLookup is an optional capability: a publication source that can resolve
// specific record ids directly (PubMed esummary). The publications command
// type-asserts for it to serve --pmid lookups.
type PMIDLookup interface {
	LookupPMIDs(ctx context.Context, pmids []string) ([]RawRecord, error)
}

// PubMed is the LIVE NCBI E-utilities adapter. Endpoints (verified live
// 2026-07-09, both keyless, retmode=json):
//
//	esearch.fcgi  db=pubmed term=<q>[Title/Abstract] -> esearchresult.{count,idlist}
//	esummary.fcgi db=pubmed id=<pmid,...>            -> result.{uids,<pmid>:{...}}
//
// Fetch is the two-step esearch -> esummary pipeline. NCBI's keyless etiquette
// cap is 3 requests/second, counted per IP across the whole process — NOT per
// Fetch. One Fetch issues at most 2 sequential requests, which was safe only
// while callers arrived one at a time; two concurrent PubMed-touching probes
// put 4 requests inside one second and NCBI answered 429 (count 4, limit 3).
// Every request here therefore goes through s.get, which paces them via
// ncbiGate. Do not call s.client.GetJSON directly.
type PubMed struct {
	client *cliutil.Client
}

func NewPubMed() *PubMed {
	return &PubMed{client: cliutil.NewClient("https://eutils.ncbi.nlm.nih.gov")}
}

func (s *PubMed) Name() string { return "pubmed" }

// IDField: the PubMed record id, flattened into each record's Raw map.
func (s *PubMed) IDField() string { return "pmid" }

// get is the single door to NCBI: it waits for this process's next E-utilities
// slot, then performs the request. Routing every call through one method is
// what makes the cap hold no matter how many goroutines are in flight.
func (s *PubMed) get(ctx context.Context, path string, params url.Values) ([]byte, int, error) {
	if err := ncbiGate.wait(ctx); err != nil {
		return nil, 0, err
	}
	return s.client.GetJSON(ctx, path, params)
}

// eutilParams carries the shared E-utilities boilerplate.
func eutilParams() url.Values {
	v := url.Values{}
	v.Set("db", "pubmed")
	v.Set("retmode", "json")
	v.Set("tool", "medical-device-intelligence-pp-cli")
	return v
}

// searchTerm quotes the device term as a phrase and scopes it to
// Title/Abstract so a device name doesn't match on unrelated metadata.
func searchTerm(term string) string {
	return fmt.Sprintf("%q[Title/Abstract]", term)
}

func (s *PubMed) Fetch(ctx context.Context, q Query) ([]RawRecord, Page, error) {
	if q.Term == "" {
		return nil, Page{}, fmt.Errorf("pubmed search requires a term")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	params := eutilParams()
	params.Set("term", searchTerm(q.Term))
	params.Set("retmax", strconv.Itoa(limit))
	if q.Skip > 0 {
		params.Set("retstart", strconv.Itoa(q.Skip))
	}
	body, _, err := s.get(ctx, "/entrez/eutils/esearch.fcgi", params)
	if err != nil {
		return nil, Page{}, err
	}
	var env struct {
		ESearchResult struct {
			Count  string   `json:"count"` // NCBI returns counts as strings
			IDList []string `json:"idlist"`
		} `json:"esearchresult"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, Page{}, err
	}
	total, _ := strconv.Atoi(env.ESearchResult.Count)
	if len(env.ESearchResult.IDList) == 0 {
		return nil, Page{Total: total}, nil // no matches -> empty, not an error
	}
	recs, err := s.LookupPMIDs(ctx, env.ESearchResult.IDList)
	if err != nil {
		return nil, Page{}, err
	}
	return recs, Page{Total: total, Returned: len(recs)}, nil
}

// pubmedSummary mirrors the slice of one esummary entry we consume.
type pubmedSummary struct {
	UID             string `json:"uid"`
	Error           string `json:"error"` // set on unknown/withdrawn pmids
	Title           string `json:"title"`
	PubDate         string `json:"pubdate"` // e.g. "2026 Jun"
	Source          string `json:"source"`  // journal abbreviation
	FullJournalName string `json:"fulljournalname"`
	LastAuthor      string `json:"lastauthor"`
	ArticleIDs      []struct {
		IDType string `json:"idtype"`
		Value  string `json:"value"`
	} `json:"articleids"`
}

// LookupPMIDs resolves PMIDs via esummary into flattened records. An unknown
// PMID comes back from NCBI as an entry with an "error" field; it is surfaced
// as a record whose error field is set, never silently dropped, so a --pmid
// caller sees exactly which ids failed to resolve. Implements PMIDLookup.
func (s *PubMed) LookupPMIDs(ctx context.Context, pmids []string) ([]RawRecord, error) {
	if len(pmids) == 0 {
		return nil, fmt.Errorf("pubmed lookup requires at least one pmid")
	}
	params := eutilParams()
	params.Set("id", strings.Join(pmids, ","))
	body, _, err := s.get(ctx, "/entrez/eutils/esummary.fcgi", params)
	if err != nil {
		return nil, err
	}
	return parseESummary(body)
}

// parseESummary decodes the esummary envelope. result.uids gives the order;
// every other key of result is one summary object keyed by pmid.
func parseESummary(body []byte) ([]RawRecord, error) {
	var env struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	var uids []string
	if raw, ok := env.Result["uids"]; ok {
		if err := json.Unmarshal(raw, &uids); err != nil {
			return nil, err
		}
	}
	recs := make([]RawRecord, 0, len(uids))
	for _, uid := range uids {
		raw, ok := env.Result[uid]
		if !ok {
			continue
		}
		var sum pubmedSummary
		if err := json.Unmarshal(raw, &sum); err != nil {
			return nil, err
		}
		journal := sum.FullJournalName
		if journal == "" {
			journal = sum.Source
		}
		doi := ""
		for _, aid := range sum.ArticleIDs {
			if aid.IDType == "doi" {
				doi = aid.Value
			}
		}
		recs = append(recs, RawRecord{
			ID: uid,
			Raw: map[string]any{
				"pmid":    uid,
				"title":   sum.Title,
				"journal": journal,
				"pubdate": sum.PubDate,
				"year":    pubYear(sum.PubDate),
				"doi":     doi,
				"error":   sum.Error,
			},
		})
	}
	return recs, nil
}

// pubYear extracts the leading 4-digit year from an NCBI pubdate ("2026 Jun").
func pubYear(pubdate string) string {
	if len(pubdate) >= 4 {
		y := pubdate[:4]
		if _, err := strconv.Atoi(y); err == nil {
			return y
		}
	}
	return ""
}

// PublicationCountWindow returns the match total for a term within a
// publication-date year range (esearch [dp] filter, verified live 2026-07-09).
// retmax=0: only the count travels.
func (s *PubMed) PublicationCountWindow(ctx context.Context, term string, fromYear, toYear int) (int, error) {
	if term == "" {
		return 0, fmt.Errorf("pubmed window count requires a term")
	}
	if fromYear > toYear {
		return 0, fmt.Errorf("pubmed window count: fromYear %d > toYear %d", fromYear, toYear)
	}
	params := eutilParams()
	params.Set("term", fmt.Sprintf("%s AND %d/01/01:%d/12/31[dp]", searchTerm(term), fromYear, toYear))
	params.Set("retmax", "0")
	body, _, err := s.get(ctx, "/entrez/eutils/esearch.fcgi", params)
	if err != nil {
		return 0, err
	}
	var env struct {
		ESearchResult struct {
			Count string `json:"count"`
		} `json:"esearchresult"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return 0, err
	}
	return strconv.Atoi(env.ESearchResult.Count)
}

func (s *PubMed) Health(ctx context.Context) error {
	params := eutilParams()
	params.Set("term", "device")
	params.Set("retmax", "1")
	_, _, err := s.get(ctx, "/entrez/eutils/esearch.fcgi", params)
	return err
}

func init() { Register(NewPubMed()) }
