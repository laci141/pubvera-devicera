# Export fix — status (2026-08-09)

Implemented in `web/index.html` (only that file touched):

1. **FIX 1 — provenance header.** New `exportMeta(key)` collects app / analysis /
   query / filters / rows_exported / rows_total / source / exported_at.
   `exportTitle(key)` builds the one-liner from it:
   `Devicera export · UDI device records · query: "stent" · showing 100 of 16,787 rows · source: openFDA · 2026-08-09 · <filters>`.
   `rows_total` stays `null` when unknown and clause 4 is then omitted.
   `deviceView.total` now carries `devicesResp.data.total`.
2. **FIX 2 — JSON shape.** `downloadJSON` for the `udi` key now emits
   `{ export: {…object…}, rows: [...] }`; `rows_total` falls back to
   `rows_exported` when unknown.
3. **FIX 3 — BibTeX year.** New `bibYear(r)` takes the leading 4 digits of the
   record's `Last Update`; the `year` line is omitted when there is no usable date.

Verified: inline `<script>` blocks parse with `node --check` (3/3 ok),
`go build ./...` ok, `go test ./...` ok.

## Remaining for tomorrow

- Produce one live export of each format (BibTeX, CSV, XLSX, JSON) from a running
  server and paste the first 15 CSV lines + the JSON `export` block as evidence.
  Server run was interrupted; restart with `go run . serve --port 8199` and search
  for "stent".
- Then commit/PR as desired. (Work is already committed on `main` locally, not pushed.)
