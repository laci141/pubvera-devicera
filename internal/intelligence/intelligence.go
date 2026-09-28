package intelligence

// This file is the package's constructor surface. Modules 02-12 export their
// analyzers from here as they land, so the CLI layer (Phase 2d) has one place
// to look.

// NewTelemetryAnalyzer builds Module 01 over any read-only Data view. Pass
// NewLiveData() for production, a mock for tests.
func NewTelemetryAnalyzer(data Data) *TelemetryAnalyzer {
	return &TelemetryAnalyzer{data: data}
}

// NewAnomalyAnalyzer builds Module 02 (trend-breaking readings: surge, new
// pattern, volume shift) over any read-only Data view.
func NewAnomalyAnalyzer(data Data) *AnomalyAnalyzer {
	return &AnomalyAnalyzer{data: data}
}

// NewCorrelationAnalyzer builds Module 03 (cross-source readings: recall
// severity, corroboration, evidence gap) over any read-only Data view.
func NewCorrelationAnalyzer(data Data) *CorrelationAnalyzer {
	return &CorrelationAnalyzer{data: data}
}

// NewManufacturerAnalyzer builds Module 05 (firm-level enforcement readings:
// recall severity, recall trend, open-recall load) over any read-only Data view.
func NewManufacturerAnalyzer(data Data) *ManufacturerAnalyzer {
	return &ManufacturerAnalyzer{data: data}
}

// NewBenchmarkAnalyzer builds Module 06 (peer readings: event-volume rank,
// severity delta vs the global mix, recall rate vs the global rate).
func NewBenchmarkAnalyzer(data Data) *BenchmarkAnalyzer {
	return &BenchmarkAnalyzer{data: data}
}

// NewClusterAnalyzer builds Module 07 (MeSH-based device neighborhoods and
// the cluster's shared MAUDE trajectory) over any read-only Data view.
func NewClusterAnalyzer(data Data) *ClusterAnalyzer {
	return &ClusterAnalyzer{data: data}
}

// NewLifecycleAnalyzer builds Module 08 (record novelty, lifecycle phase,
// recall recency) over any read-only Data view.
func NewLifecycleAnalyzer(data Data) *LifecycleAnalyzer {
	return &LifecycleAnalyzer{data: data}
}

// NewFailureModeAnalyzer builds Module 09 (top problems, problem
// concentration, new problem modes) over any read-only Data view.
func NewFailureModeAnalyzer(data Data) *FailureModeAnalyzer {
	return &FailureModeAnalyzer{data: data}
}

// NewResearchAnalyzer builds Module 10 (publication momentum, active-trial
// share, trial attrition) over any read-only Data view.
func NewResearchAnalyzer(data Data) *ResearchAnalyzer {
	return &ResearchAnalyzer{data: data}
}

// NewReportingAnalyzer builds Module 11 (independent-reporting share,
// missing-date completeness, maker concentration) over any read-only Data view.
func NewReportingAnalyzer(data Data) *ReportingAnalyzer {
	return &ReportingAnalyzer{data: data}
}

// NewSynthesisAnalyzer builds Module 12 (the full-dossier capstone) over any
// read-only Data view.
func NewSynthesisAnalyzer(data Data) *SynthesisAnalyzer {
	return &SynthesisAnalyzer{data: data}
}
