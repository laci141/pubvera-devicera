# medical-device-intelligence

Multi-source medical device intelligence CLI (Go).

## Features

- 23 commands + 12 intelligence modules
- Live sources: openFDA (device/MAUDE/UDI; keyless unless `OPENFDA_API_KEY` is set), ClinicalTrials.gov v2 and PubMed (keyless)
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

## openFDA API key (optional)

Without a key, openFDA allows a limited number of requests per day per IP
address, shared by everything that runs on that IP. A free key raises the
daily limit. Request one at https://open.fda.gov/apis/authentication/ and set
it in the environment:

```sh
OPENFDA_API_KEY=<your key> mdi serve
```

- Read once at startup; unset or empty means keyless, exactly as before.
- Sent as the `api_key` query parameter on api.fda.gov requests only — never
  to ClinicalTrials.gov or PubMed, and never in the browser's verify links.
- Never logged and never echoed in error messages.
- Never commit it: on the server it lives in the app's `.env` file (mode 0600),
  referenced from `docker-compose.yml` as `${OPENFDA_API_KEY}`. Same variable
  name as the other Pubvera apps that call openFDA.

## Intelligence Modules (12)

01 Telemetry, 02 Anomaly, 03 Correlation, 04 Compliance, 05 Manufacturing,
06 Benchmark, 07 Clustering, 08 Lifecycle, 09 FailureMode, 10 Research,
11 Reporting, 12 Synthesis

## Disclaimer

Educational + research use only. Signals are documentation readings,
NOT medical or safety advice.

## License

MIT
