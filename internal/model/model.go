// Package model holds the canonical structs shared across every data source and
// regulator adapter. These are the join targets the intelligence modules
// consume; source-specific envelopes are normalized into these shapes.
package model

// SourceRef records where a fact came from. Every record carries one so that
// "cite the source record id" is satisfied structurally, not per command.
type SourceRef struct {
	Source    string `json:"source"`     // e.g. "openfda_device_enforcement"
	RecordID  string `json:"record_id"`  // the source's own id (recall_number, nct_id, pmid, ...)
	FetchedAt string `json:"fetched_at"` // RFC3339
	URL       string `json:"url,omitempty"`
}

// Device is the canonical entity — the join target across sources.
type Device struct {
	DeviceKey        string    `json:"device_key"` // resolution key (product_code|udi_di)
	Name             string    `json:"name"`
	Brand            string    `json:"brand,omitempty"`
	Manufacturer     string    `json:"manufacturer,omitempty"`
	ProductCode      string    `json:"product_code,omitempty"`
	UDIDI            string    `json:"udi_di,omitempty"`
	FDAClass         string    `json:"fda_class,omitempty"` // "Class I" | "Class II" | "Class III"
	RegulatoryStatus string    `json:"regulatory_status,omitempty"`
	Source           SourceRef `json:"source"`
}

// UDI is a GUDID device-identifier profile.
type UDI struct {
	UDIDI          string    `json:"udi_di"`
	DeviceKey      string    `json:"device_key"`
	Manufacturer   string    `json:"manufacturer,omitempty"`
	Model          string    `json:"model,omitempty"`
	CommercialName string    `json:"commercial_name,omitempty"`
	Source         SourceRef `json:"source"`
}

// Recall is an openFDA device recall/enforcement record.
type Recall struct {
	RecallNumber         string    `json:"recall_number"`
	DeviceKey            string    `json:"device_key,omitempty"`
	Classification       string    `json:"classification,omitempty"`
	RecallingFirm        string    `json:"recalling_firm,omitempty"`
	ProductDescription   string    `json:"product_description,omitempty"`
	ReasonForRecall      string    `json:"reason_for_recall,omitempty"`
	RecallInitiationDate string    `json:"recall_initiation_date,omitempty"`
	Status               string    `json:"status,omitempty"`
	Source               SourceRef `json:"source"`
}

// Event is a MAUDE adverse-event record (aggregate-friendly).
type Event struct {
	MAUDEID       string    `json:"maude_id"`
	DeviceKey     string    `json:"device_key,omitempty"`
	ReportDate    string    `json:"report_date,omitempty"`
	EventType     string    `json:"event_type,omitempty"`
	DeviceProblem string    `json:"device_problem,omitempty"`
	Source        SourceRef `json:"source"`
}

// Trial is a ClinicalTrials.gov study.
type Trial struct {
	TrialID      string    `json:"trial_id"` // NCT id
	DeviceKey    string    `json:"device_key,omitempty"`
	Phase        string    `json:"phase,omitempty"`
	Status       string    `json:"status,omitempty"`
	Condition    string    `json:"condition,omitempty"`
	Intervention string    `json:"intervention,omitempty"`
	Source       SourceRef `json:"source"`
}

// Publication is a PubMed/OpenAlex/Crossref record.
type Publication struct {
	ID        string    `json:"id"` // pmid or doi or openalex id
	DeviceKey string    `json:"device_key,omitempty"`
	PMID      string    `json:"pmid,omitempty"`
	DOI       string    `json:"doi,omitempty"`
	Title     string    `json:"title,omitempty"`
	Year      int       `json:"year,omitempty"`
	Citations int       `json:"citations,omitempty"`
	Source    SourceRef `json:"source"`
}

// RegulatoryAction is the multi-agency spine. FDA/EMA/HealthCanada/TGA/PMDA all
// normalize into this one shape, so a single table and query path serve every
// agency.
type RegulatoryAction struct {
	Agency       string    `json:"agency"`       // FDA | EMA | HealthCanada | TGA | PMDA
	Jurisdiction string    `json:"jurisdiction"` // US | EU | CA | AU | JP
	DeviceKey    string    `json:"device_key,omitempty"`
	ActionType   string    `json:"action_type"` // approval|clearance|recall|warning|withdrawal|classification
	Status       string    `json:"status,omitempty"`
	Date         string    `json:"date,omitempty"`
	Reference    string    `json:"reference,omitempty"` // agency record id
	URL          string    `json:"url,omitempty"`
	Source       SourceRef `json:"source"`
}

// DeviceDossier is the assembled join the intelligence modules operate on.
type DeviceDossier struct {
	Device       Device             `json:"device"`
	UDIs         []UDI              `json:"udis,omitempty"`
	Recalls      []Recall           `json:"recalls,omitempty"`
	Events       []Event            `json:"events,omitempty"`
	Trials       []Trial            `json:"trials,omitempty"`
	Publications []Publication      `json:"publications,omitempty"`
	Regulatory   []RegulatoryAction `json:"regulatory,omitempty"`
}
