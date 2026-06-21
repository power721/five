# Index115 TVBox Consumption (alist-tvbox) Design

## Goal

Let the TVBox end user browse, search, and play the 115 index through
alist-tvbox, by treating PowerList as a version-1 `Site` whose browse/search/play
route to PowerList's `/index115` API.

## Background

PowerList (OpenList-based) serves (auth-gated):
- `GET /index115/browse?share_code=&receive_code=&parent_id=` → `[]FileItem`.
  Empty `share_code` lists all shares (root); else lists children of
  `share_code` + `parent_id` (default `"0"`).
- `GET /index115/search?q=&page=&per_page=&share_code=` → `{query,total,items:[]FileItem}`.
- `POST /index115/link` body `{cookie, share_code, receive_code, file_id}` → `{url, expired_in}`.

`FileItem` carries `FileID, ShareCode, ReceiveCode, Name, Path, Size, IsDir, Ext`.
alist-tvbox already downloads the index to `/data/index115`.

## Scope

Owns:
- An `Index115Client` calling PowerList `/index115/browse|search|link`.
- Version-1 branches in `TvBoxService.getMovieList`, `getPlayUrl`, `searchByApi`.
- An identity-encoding scheme threaded through the existing `proxyService`.
- Seeding the `index115` Site (version 1) with the shared AList token.

Does NOT own:
- The PowerList `/index115` API (already implemented).
- Index download (already built).

## Design

### Discriminator

`site.getStorageVersion() == 1`. `AListService.getVersion` short-circuits on
non-null `storageVersion`, so it returns 1 without probing — but version-1 sites
never reach `AListService` anyway (the `TvBoxService` branches intercept first).

### Why `TvBoxService` branches (not `AListService`)

`getMovieList` builds each child's `vod_id` from
`proxyService.generatePath(site, parentPath + "/" + fsInfo.getName())`, and
`FsInfo` has only `name` (no path/id field). So the index115 identity
(`shareCode`/`receiveCode`/`fileID`) would be lost in the generic path build.
Branching in `TvBoxService` lets us encode the identity into the path that
`proxyService` stores and recover it on drill-in/play.

### Identity encoding

Path form: `/idx/<shareCode>:<receiveCode>/<id>` where `id` is a `parentID`
(browse) or `fileID` (play); root is `/`. These path strings are registered via
`proxyService.generatePath(site, path)` (which stores arbitrary `(site,path)→int`,
`int→path` in the `PlayUrl` table) exactly like any other site. `receive_code`
in the path is consistent with how share passwords already flow through TVBox
paths.

### `Index115Client` (new, mirrors `AListService`)

Same `RestTemplateBuilder`/header pattern; `Authorization: site.getToken()`.
- `browse(Site, shareCode, receiveCode, parentID) -> List<Index115File>` — `GET {url}/index115/browse`.
- `search(Site, query, page, perPage) -> Index115SearchResult` — `GET {url}/index115/search`.
- `resolveLink(Site, cookie, shareCode, receiveCode, fileID) -> String url` — `POST {url}/index115/link`.

`Index115File` mirrors PowerList's `FileItem`.

### `TvBoxService` branches (version-1)

- `getMovieList`: decode `path` (root `/` → shares; `/idx/<sc>:<rc>` → share root
  `parent_id="0"`; `/idx/<sc>:<rc>/<parent>` → children). For each `FileItem`,
  `vod_name` = display name, `vod_id` = `siteId + "$" + proxyService.generatePath(site, encodedChildPath) + "$1"`.
- `getPlayUrl`: decode `path` → `(shareCode, receiveCode, fileID)`; call
  `index115Client.resolveLink(...)`. The cookie is the master Pan115 account's
  (`driverAccountRepository.findByTypeAndMasterTrue(DriverType.PAN115)`).
  Return `{parse:0, url, type: PAN115, header:{User-Agent, Referer:115.com}}`.
- `searchByApi`: `index115Client.search(...)` → `MovieDetail`s with `vod_id` from
  encoded child paths.

### Site seed

Idempotent startup seed creates, if absent, a Site `name="index115"`,
`storageVersion=1`, `url="http://localhost"`, `searchable=true`,
`token=<shared AList token>` (read from the `alist_token` setting; `resetToken`
keeps it in sync). Auto-appears as a TVBox category.

### Data flow

```
TVBox -> /vod/{token} -> TvBoxController -> TvBoxService
  category list  -> getCategoryList: version-1 site auto-listed (root "/")
  drill-in       -> getMovieList: version==1 ? Index115Client.browse (decode path)
                                          : AListService.listFiles
  search         -> searchByApi:   version==1 ? Index115Client.search
                                          : AListService.search
  play           -> getPlayUrl:    version==1 ? Index115Client.resolveLink (Pan115 cookie)
                                          : AListService.getFile
```

## Error handling

PowerList errors map to the same `BadRequestException` path AList uses. A
down/slow PowerList degrades one category, not the whole config. Missing master
Pan115 cookie at play time → clear error.

## Testing

- `Index115ClientTest` (MockRestServiceServer): browse/search/link mapping.
- `TvBoxServiceTest` (mocked `Index115Client` + `proxyService` + repos):
  version-1 root lists shares; drill-in encodes child paths; play decodes + calls
  link with the Pan115 cookie; search maps results. Version-3 sites still hit AList.
- Seed test: creates the site when absent, idempotent, token set.

## Open items (pin during implementation)

1. Exact `FileItem` field JSON names in PowerList responses (camel_case vs
   snake_case) — read PowerList's struct tags.
2. `browse`/`search` query-param names match the handler (`share_code`,
   `receive_code`, `parent_id`, `q`, `page`, `per_page`).
3. Confirm `http://localhost` (no port) reaches PowerList, or set the real port
   on the seeded site.
