# Index115 TVBox Consumption (alist-tvbox) Design

## Goal

Let the TVBox end user browse and search the 115 index through alist-tvbox, by
treating PowerList as a version-1 `Site` whose browse/search route to its
`/index115` API while playback uses its AList-compatible `/api/fs/get`.

## Background

PowerList (OpenList-based) serves `/index115/browse`, `/index115/search`,
`/index115/link` (auth-gated, under `auth.Group("/index115")`). It also serves the
standard AList API (`/api/fs/get`, etc.) because it is an AList fork. alist-tvbox
already downloads the index to `/data/index115` (see
`2026-06-21-115-index-publish-design.md`).

## Scope

Owns:
- An `Index115Client` that calls PowerList `/index115/browse` and `/index115/search`.
- Branching `AListService.listFiles` and `AListService.search` to the client when
  `site.getStorageVersion() == 1`.
- Seeding an `index115` Site (version 1, url `http://localhost`, searchable) with
  the shared AList token.

Does NOT own:
- Playback wiring — PowerList plays via the unchanged AList path (`/api/fs/get`).
- The PowerList `/index115` API itself (already implemented in PowerList).
- Index download (already built: `Index115Service`).

## Design

### Discriminator

`site.getStorageVersion() == 1` marks a PowerList/index115 backend. AList sites
use 2 or 3. `AListService.getVersion` already short-circuits
(`if storageVersion != null return it`), so it returns 1 without probing
`/api/public/settings` — no change needed there.

### `Index115Client` (new, mirrors `AListService`)

- `browse(Site site, String path, int page, int size) -> FsResponse` —
  `GET {site.url}/index115/browse` with `Authorization: {site.getToken()}`,
  maps the response JSON to a `FsInfo` list so `TvBoxService` reuses the existing
  `FsInfo -> MovieDetail` conversion.
- `search(Site site, String keyword) -> List<SearchResult>` —
  `GET {site.url}/index115/search`, mapped to `SearchResult`.

Built with the same `RestTemplateBuilder`/header pattern as `AListService`.

### `AListService` branches (two only)

- `listFiles`: after `int version = getVersion(site);`, add
  `if (version == 1) return index115Client.browse(site, path, page, size);`
  before the existing `/api/fs/list` logic.
- `search`: at the top, add
  `if (site.getStorageVersion() != null && site.getStorageVersion() == 1) return index115Client.search(site, keyword);`

`getFile`/playback, rename, move, remove, etc. are untouched — they already work
against PowerList's AList-compatible endpoints. Every `TvBoxService` call site
(root category list, drill-in, `dfs`, subtitles, `searchByApi`) inherits the
switch automatically because it goes through these two methods.

### Site seed

An idempotent startup seed (e.g. `ApplicationRunner`) creates, if absent, a Site:
`name = "index115"`, `storageVersion = 1`, `url = "http://localhost"`,
`searchable = true`, `token = <current AList token>`. The token is read the same
way site id=1 gets it (`SiteService`'s shared `aListToken` / the `alist_token`
setting); `SiteService.resetToken` already re-syncs every site carrying that
token, so rotation is handled. The site auto-appears as a TVBox category via
`getCategoryList`.

### Data flow

```
TVBox -> /vod/{token} -> TvBoxController -> TvBoxService
  category list / drill-in -> AListService.listFiles(site,...)
                                version==1 ? Index115Client.browse -> /index115/browse
                                           : AList /api/fs/list
  search               -> AListService.search(site,...)
                                version==1 ? Index115Client.search -> /index115/search
                                           : AList /api/fs/search
  play                 -> AListService.getFile -> http://localhost/api/fs/get  (unchanged)
```

## Error handling

PowerList errors map to the same `BadRequestException` path AList uses
(`logError`). A down/slow PowerList degrades one category, not the whole config.

## Testing

- `Index115ClientTest` with `MockRestServiceServer` (browse + search mapping).
- `AListServiceTest`: a version-1 site routes `listFiles`/`search` to
  `Index115Client` (mocked); a version-3 site still hits AList.
- Seed test: creates the site when absent, leaves it when present, sets the token.

## Open items (pin during implementation)

1. PowerList `/index115/browse` and `/index115/search` JSON shapes → exact field
   mapping to `FsInfo` / `SearchResult` (read PowerList `handles.Index115Browse`
   / `Index115Search` and the `BrowseRequest`/`SearchRequest`/`FileItem` types).
2. `browse` request parameters (path / page / size naming) must match what
   PowerList's handler expects.
3. Confirm `http://localhost` (no port) is the right base for the deployment, or
   whether alist-tvbox reaches PowerList on a specific port.
