# medical-device-intelligence

Multi-source medical device intelligence CLI (Go).

## Features

- 23 commands + 12 intelligence modules
- Keyless live sources: openFDA (device/MAUDE/UDI), ClinicalTrials.gov v2, PubMed
- SQLite cache (`sync` / `watch` / `export`)
- Explainable Signals (sample size + cited sources, NEVER a risk score)

## Install

```sh
go build -o mdi ./cmd/medical-device-intelligence-pp-cli
```

## Usage examples

```sh
mdi signals --device pacemaker
mdi dossier --device pacemaker --json
mdi compare pacemaker stent
```

## Intelligence Modules (12)

01 Telemetry, 02 Anomaly, 03 Correlation, 04 Compliance, 05 Manufacturing,
06 Benchmark, 07 Clustering, 08 Lifecycle, 09 FailureMode, 10 Research,
11 Reporting, 12 Synthesis

## Disclaimer

Educational + research use only. Signals are documentation readings,
NOT medical or safety advice.

## License

MIT
