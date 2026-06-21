# 115 Index Publish/Download Pipeline Design

## Goal

Distribute the `five` index (SQLite `file`/`share` tables + Bleve index) to the
alist-tvbox container as `115.index.zip`, over a 115 share channel, so that
PowerList can read a freshly built `/data/index115` without shared storage.

End-to-end:

```
five export-db  ->  115.index.zip (trimmed index.db + bleve/)
(manual)        ->  upload zip to 115, create share, write 115.version.txt = shareCode:receiveCode
alist-tvbox     ->  fetch 115.version.txt, receive share into own 115 (/alist-tvbox-temp),
                    download, delete, extract to /data/index115
PowerList       ->  reads /data/index115 (unchanged consumer; see index115-consumer-design.md)
```

## Scope

This design owns:

- `five`: producing `115.index.zip` from the live DB + READY Bleve index.
- `alist-tvbox`: a manual endpoint that fetches `115.version.txt`, receives the
  published share into the user's own 115 via the bundled AList drivers,
  downloads, deletes, and extracts to `/data/index115` atomically.
- The `115.version.txt` contract (`shareCode:receiveCode`, one line).
- The `115.index.zip` layout (`index.db` + `bleve/`).

This design does NOT own:

- The PowerList consumer (covered by `2026-06-19-index115-consumer-design.md`).
- Automatic upload-to-115 / share creation / `115.version.txt` publishing on the
  `five` side (manual runbook only — explicit decision).
- Crawling, index building, or the bleve mapping (existing `five` behavior).
- Scheduled or startup-triggered downloads (manual endpoint only).

## Boundary

`five` is the only producer. `115.index.zip` is the immutable handoff artifact.
`115.version.txt` is the version pointer. alist-tvbox is the only downloader.

```text
five
  -> export-db builds trimmed index.db (file + share only)
  -> packages index.db + manifest.IndexPath contents into 115.index.zip

publisher (manual)
  -> uploads 115.index.zip to a 115 account
  -> creates a 115 share
  -> overwrites d.example.com/115.version.txt with "shareCode:receiveCode"

alist-tvbox  Index115Service
  -> GET 115.version.txt, parse shareCode:receiveCode
  -> compare stored shareCode; skip if equal
  -> mount Pan115Share, AList copy zip into Pan115:/alist-tvbox-temp
  -> download to local temp, delete from 115
  -> extract to /data/index115 (atomic swap), persist shareCode

PowerList
  -> reads /data/index115/index.db + /data/index115/bleve (unchanged)
```

## Component: `five` packaging (`export-db` mode)

### Store primitive

Add `Store.ExportTrimmed(ctx context.Context, destPath string) error`:

1. `VACUUM INTO` a temp full single-file copy (reuse the existing
   `ExportSnapshot` mechanism).
2. Open the copy; `DROP TABLE crawl_checkpoint, index_event, index_manifest, kv`
   and `DROP INDEX idx_index_event_processed_id`.
3. `VACUUM` to compact and reclaim freed pages.
4. Close.

The result contains only `file` and `share` (with `idx_file_share_parent`,
`idx_file_share_path`, `idx_file_ext`, `idx_file_depth`, `idx_file_size`, and the
share `UNIQUE(share_code, receive_code)`). No crawler/indexer internals leak to
consumers. The file is single-file after close (no `-wal`/`-shm`).

`Store.ExportSnapshot` (raw full copy) stays unchanged; it remains a primitive
used internally and keeps its existing test green.

### Mode wiring (`cmd/115-indexer/main.go`, `case "export-db"`)

- Require `-out` (the zip path; semantics change from raw `.db` to `.zip`).
- Resolve the Bleve source:
  - prefer the `READY` `manifest.IndexPath`;
  - else fall back to the newest `index_*` dir under the `-bleve` flag, with a
    warning;
  - else fatal with "run rebuild-index first".
- In a temp dir:
  1. `s.ExportTrimmed(ctx, tmp/index.db)`.
  2. `buildPackage(tmp/index.db, bleveSrcDir, *outPath)` — writes the zip.
- Print a one-line summary.

### Zip layout (flat)

```
115.index.zip
  index.db          <- the trimmed DB (only file + share)
  bleve/...         <- contents of the READY bleve index dir (manifest.IndexPath)
```

Extracts to `<dir>/index.db` + `<dir>/bleve/`, matching PowerList's
`Index115.DBFile` / `Index115.BleveDir` at `/data/index115`.

Refactor the zip step into a pure, testable function
`buildPackage(trimmedDBPath, bleveSrcDir, zipDestPath string) error` so layout is
unit-testable without a live store.

## Component: manual publish to 115 (runbook, no code)

1. `./five -mode export-db -db data/index.db -bleve data/bleve -out 115.index.zip`
2. Upload `115.index.zip` to the publishing 115 account; create a share.
3. Overwrite `https://d.example.com/115.version.txt` with one line
   `shareCode:receiveCode` (e.g. `swf01d43zby:6666`).

Documented in `README.md`. Deliberately manual — no automation in `five`.

## Component: alist-tvbox download (`Index115Service`)

### Trigger & state

- New `@Service Index115Service` and `Index115Controller`
  (`POST /api/index115/update`), behind the existing alist-tvbox admin auth.
- New `TaskType INDEX115`; the endpoint starts a task and returns its id
  (consistent with `IndexFileService.validateIndexFiles`).
- Persist last applied share code in `SettingService` under `index115.share_code`.

### Flow

1. Fetch `https://d.example.com/115.version.txt` (existing RestTemplate pattern),
   trim, split on `:` into `shareCode` / `receiveCode`; require both non-empty.
2. If equal to stored `index115.share_code` -> complete task "up to date", return.
3. Register the published share as a **`Pan115Share`** storage mount (reuse the
   `ShareService` / `DriverAccountService` storage-registration pattern).
4. AList cross-storage **copy** (`/api/fs/copy`) of `115.index.zip` from the
   share mount into the user's own **`Pan115`** mount at `/alist-tvbox-temp/`.
5. **Download** `/alist-tvbox-temp/115.index.zip` to a local temp file via
   `AListLocalService` streaming.
6. **Delete** `/alist-tvbox-temp/115.index.zip` via AList `/api/fs/remove`.
7. Extract the zip to `/data/index115.new`, then atomic swap:
   `/data/index115` -> `/data/index115.old`, `/data/index115.new` ->
   `/data/index115`, remove `/data/index115.old`.
8. Persist `index115.share_code = shareCode`; complete the task.

Reuses `Pan115Share` + `Pan115` drivers and the bundled AList fs/admin APIs. No
new 115 wire protocol is written.

### Assumptions

- The bundled AList has the user's own 115 storage registered (`Pan115`) with
  `/alist-tvbox-temp` present.
- `AListLocalService` exposes (or will be augmented with) copy / download /
  remove / storage-registration helpers; exact method wiring is a plan detail.
- `/data/index115` is the container path PowerList reads (host volume mapping is
  a deployment concern).

## Error handling

- **five:** trim or zip failure, or no resolvable Bleve source -> `log.Fatalf`
  with a clear message. Temp dir cleaned via defer.
- **alist-tvbox:** any failure fails the task with a message and leaves the
  existing `/data/index115` intact. Cleanup best-effort: remove the partial file
  from `/alist-tvbox-temp`, remove the local temp, remove `/data/index115.new`.
  Skip-on-equal is success, not an error.

## Testing

- **five:**
  - `TestExportTrimmedKeepsOnlyFileAndShare` — populate all tables; assert the
    result has only `file` + `share`, matching row counts, the `idx_file_*`
    indexes present, and no `-wal`/`-shm` sidecar after close.
  - `TestBuildPackageZipLayout` — zip contains `index.db` and a `bleve/` tree
    whose entries match the source dir contents.
  - Existing `TestSQLiteStoreExportSnapshotIsSelfContained` stays green.
- **alist-tvbox:**
  - `Index115ServiceTest` with mocked RestTemplate / AListLocalService /
    SettingService: parse success/failure, skip-when-equal, invoke-flow-when-
    changed.
  - Pure helper test for extract + atomic swap (zip -> dir, rename).
  - Controller test for the endpoint shape/auth.
  - Live 115 ops verified manually.

## Open items (verify during implementation)

1. **Bleve version skew:** `five` builds on `bleve/v2 v2.6.0`; PowerList opens
   on `v2.5.2`. Confirmed working today; treat as a checkpoint, do not pin.
2. **AList helper coverage:** confirm `AListLocalService` (or augment it) covers
   cross-storage copy, file download, remove, and temporary storage mount.
3. **Auth:** confirm `/api/index115/update` sits behind the existing admin auth
   filter like other admin endpoints.
