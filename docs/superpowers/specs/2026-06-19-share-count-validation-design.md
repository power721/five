# Share Count Validation Design

## Goal

Add a validation flow to `cmd/115-indexer` that compares, for each registered share, the total item count reported by the 115 `share/snap` API with the number of rows currently stored in the local `file` table, then outputs only the shares whose counts do not match.

## Scope

This design owns:

- A new CLI mode for share count validation.
- A store-level query for counting indexed files by `share_code`.
- Per-share comparison and mismatch reporting.
- End-of-run summary output for validated, mismatched, and failed shares.

This design does not own:

- Scheduler integration.
- Admin HTTP endpoints.
- Persistent validation history.
- Automatic repair or recrawl behavior.
- Schema changes.

## Entry Point

Add a new CLI mode:

```text
-mode validate-share-counts
```

The mode reads shares from the existing SQLite `share` table through `store.ListShares(ctx)`. It does not read from `115_shares.txt`, because the validation target should be the set of shares already registered in the database and expected to have indexed `file` rows.

## Validation Flow

For each share:

1. Load `share_code`, `receive_code`, and `share_title` from the database.
2. Call the existing 115 `share/snap` API at the share root with:
   - `cid=0`
   - `offset=0`
   - `limit=1`
3. Read `data.count` from the response as the 115-reported total count for the share root.
4. Query SQLite for `COUNT(*)` from `file` where `share_code = ?`.
5. Compare the two counts.
6. Output the share only when the counts are different.

The request uses `limit=1` because only the count metadata is needed. This keeps validation requests minimal while still returning the root `data.count`.

## Output Contract

Mismatch output is line-oriented and stable for shell usage:

```text
share=<share_code> api_count=<count> db_count=<count> title="<share_title>"
```

Per-share failures are also line-oriented:

```text
share=<share_code> validate_failed error="<error>"
```

At the end of the run, always print a summary:

```text
validated=<n> mismatched=<n> failed=<n>
```

Definitions:

- `validated`: shares whose API count and DB count were both obtained successfully.
- `mismatched`: validated shares whose counts differ.
- `failed`: shares whose API call or DB count query failed.

## Error Handling

Validation is best-effort across the full share set.

- A single share failure must not abort the run.
- API dead-share or invalid-code responses are treated as per-share failures and counted in `failed`.
- Database query failures for one share are treated as per-share failures and counted in `failed`.
- Only unrecoverable setup failures should terminate the command, for example:
  - database open failure
  - inability to construct the 115 client
  - failure to list shares from the database

This keeps the mode useful as an audit tool even when some shares are stale, dead, or temporarily inaccessible.

## Store Surface

Add a store method:

```go
func (s *Store) CountFilesByShare(ctx context.Context, shareCode string) (int, error)
```

Behavior:

- Returns `COUNT(*)` from `file` filtered by `share_code`.
- Returns `0, nil` when no rows exist for that share.

The query belongs in `internal/store` so the CLI flow depends on a stable storage interface instead of embedding SQL in `main`.

## Client Surface

No API client changes are required. The mode reuses `api115.Client.List` with a root request:

```go
api115.ListRequest{
    ShareCode:   share.ShareCode,
    ReceiveCode: share.ReceiveCode,
    CID:         "0",
    Offset:      0,
    Limit:       1,
}
```

The existing response parser already exposes `SnapResponse.Data.Count`, so no response model change is needed.

## Testing

Follow TDD:

1. Add a store unit test covering `CountFilesByShare`.
2. Add a CLI-level unit test for the validation flow using:
   - a fake 115 fetcher returning configured `data.count`
   - a fake store or small in-memory stub returning configured DB counts
3. Verify:
   - mismatches are emitted
   - matches are suppressed
   - per-share failures do not stop later shares
   - summary counts are correct

Implementation should keep the validation loop in a small helper so the behavior can be tested without invoking full process-level flag parsing.

## Operational Notes

- This mode accesses 115 directly, so it should follow the same proxy and cookie conventions already used for other direct-access modes.
- The command is read-only with respect to share and file data.
- The command is intended for audit output, not for automatic reconciliation.
