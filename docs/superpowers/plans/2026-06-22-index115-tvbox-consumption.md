# Index115 TVBox Consumption Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let TVBox browse/search/play the 115 index through alist-tvbox by treating PowerList as a version-1 `Site` whose browse/search/play route to PowerList `/index115`.

**Architecture:** New `Index115Client` (PowerList `/index115` calls) + `Index115TvBoxAdapter` (version-1 list/play/search, threads `(shareCode,receiveCode,fileID)` identity through `proxyService` via encoded paths) + thin version-1 branches in `TvBoxService.getMovieList/getPlayUrl/searchByApi` + a seeded `index115` site.

**Tech Stack:** Java 21, Spring Boot, JPA, Mockito + MockRestServiceServer, JUnit 5. Root `/home/user/workspace/alist-tvbox`, package `cn.har01d.alist_tvbox`.

**Spec:** `docs/superpowers/specs/2026-06-22-index115-tvbox-consumption-design.md`

**Verified facts:**
- `FsInfo` has only `name` (no path/id) → identity must be encoded in the path.
- `proxyService.generatePath(Site, String path) -> int` / `getPath(int) -> String` (DB-backed `PlayUrl`; accepts arbitrary path strings).
- `getMovieList` builds `vod_id = site.getId() + "$" + pid + "$1"`, `pid = proxyService.generatePath(site, parentPath + "/" + name)`.
- `MovieList{page,pagecount,limit,total,List<MovieDetail> list}`.
- `vod_tag` constants: `cn.har01d.alist_tvbox.util.Constants.FOLDER` / `.FILE`.
- Pan115 cookie: `driverAccountRepository.findByTypeAndMasterTrue(DriverType.PAN115)`.
- PowerList JSON: `{"code":200,"data":...}`; `FileItem` fields are PascalCase (`FileID`,`ShareCode`,`ReceiveCode`,`Name`,`Path`,`Size`,`IsDir`,`Ext`).
- `getCategoryList` auto-lists every Site as category `siteId$/$1` → drilled-in `getMovieList` gets `path="/"`.

---

## File structure

- Create `dto/Index115File.java` — PowerList FileItem.
- Create `dto/Index115Response.java` — generic `{code,message,data}`.
- Create `dto/Index115SearchData.java` — `{total, items}`.
- Create `dto/Index115LinkData.java` — `{url, expired_in}`.
- Create `service/Index115PathCodec.java` — encode/decode `/idx/<sc>:<rc>/<id>`.
- Create `service/Index115Client.java` — browse/search/resolveLink.
- Create `service/Index115TvBoxAdapter.java` — version-1 list/play/search.
- Modify `service/TvBoxService.java` — inject adapter + 3 branches.
- Modify `service/Index115Config.java` — client + adapter beans.
- Create `service/Index115SiteSeed.java` — seed the site.
- Tests under `src/test/java/cn/har01d/alist_tvbox/service/`.

---

### Task 1: `Index115PathCodec`

**Files:** Create `service/Index115PathCodec.java`; Test `service/Index115PathCodecTest.java`.

- [ ] **Step 1: Failing test**

```java
package cn.har01d.alist_tvbox.service;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class Index115PathCodecTest {
    @Test
    void rootDecodesToNull() {
        assertNull(Index115PathCodec.decode(null));
        assertNull(Index115PathCodec.decode(""));
        assertNull(Index115PathCodec.decode("/"));
    }

    @Test
    void shareRootRoundTrips() {
        String p = Index115PathCodec.shareRoot("sw1", "6666");
        String[] d = Index115PathCodec.decode(p);
        assertEquals("sw1", d[0]);
        assertEquals("6666", d[1]);
        assertNull(d[2]);
    }

    @Test
    void childRoundTrips() {
        String p = Index115PathCodec.child("sw1", "6666", "file42");
        String[] d = Index115PathCodec.decode(p);
        assertEquals("sw1", d[0]);
        assertEquals("6666", d[1]);
        assertEquals("file42", d[2]);
    }

    @Test
    void nonIndexReturnsNull() {
        assertNull(Index115PathCodec.decode("/Movies/2024"));
    }
}
```

- [ ] **Step 2: Run, verify fail** — `./mvnw -q -Dtest=Index115PathCodecTest test` → FAIL (class missing).

- [ ] **Step 3: Implement**

```java
package cn.har01d.alist_tvbox.service;

/** Encodes index115 identity into TVBox paths threaded through proxyService.
 *  Root "/" = list shares. "/idx/<sc>:<rc>" = share root (parent "0").
 *  "/idx/<sc>:<rc>/<id>" = id is parentID (browse) or fileID (play). */
public final class Index115PathCodec {
    private static final String PREFIX = "/idx/";

    private Index115PathCodec() {}

    public static String shareRoot(String shareCode, String receiveCode) {
        return PREFIX + shareCode + ":" + receiveCode;
    }

    public static String child(String shareCode, String receiveCode, String id) {
        return PREFIX + shareCode + ":" + receiveCode + "/" + id;
    }

    /** @return null for root/non-index paths; else {shareCode, receiveCode, id} (id null at share root). */
    public static String[] decode(String path) {
        if (path == null || path.isEmpty() || path.equals("/") || !path.startsWith(PREFIX)) {
            return null;
        }
        String rest = path.substring(PREFIX.length());
        int slash = rest.indexOf('/');
        String head = slash < 0 ? rest : rest.substring(0, slash);
        String id = slash < 0 ? null : rest.substring(slash + 1);
        int colon = head.indexOf(':');
        if (colon <= 0) {
            return null;
        }
        return new String[]{head.substring(0, colon), head.substring(colon + 1), id};
    }
}
```

- [ ] **Step 4: Run, verify pass** → PASS.
- [ ] **Step 5: Commit** — `git add ... && git commit -m "feat: Index115PathCodec encodes share/file identity into paths"`.

---

### Task 2: DTOs + `Index115Client`

**Files:** Create `dto/Index115File.java`, `dto/Index115Response.java`, `dto/Index115SearchData.java`, `dto/Index115LinkData.java`, `service/Index115Client.java`; Test `service/Index115ClientTest.java`.

- [ ] **Step 1: Failing test**

```java
package cn.har01d.alist_tvbox.service;

import cn.har01d.alist_tvbox.entity.Site;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.http.MediaType;
import org.springframework.test.web.client.MockRestServiceServer;
import org.springframework.web.client.RestTemplate;

import static org.junit.jupiter.api.Assertions.*;
import static org.springframework.test.web.client.match.MockRestRequestMatchers.*;
import static org.springframework.test.web.client.response.MockRestResponseCreators.withSuccess;

class Index115ClientTest {
    private RestTemplate rt;
    private Index115Client client;
    private MockRestServiceServer server;
    private final Site site = site();

    @BeforeEach
    void setup() {
        rt = new RestTemplate();
        client = new Index115Client(rt);
        server = MockRestServiceServer.createServer(rt);
    }

    @Test
    void browseRootReturnsShares() {
        server.expect(requestTo("http://p/index115/browse?share_code=&receive_code=&parent_id="))
                .andRespond(withSuccess("{\"code\":200,\"data\":[{\"FileID\":\"\",\"ShareCode\":\"sw1\",\"ReceiveCode\":\"6666\",\"Name\":\"Lib\",\"IsDir\":true}]}", MediaType.APPLICATION_JSON));
        var items = client.browse(site, "", "", "");
        server.verify();
        assertEquals(1, items.size());
        assertEquals("sw1", items.get(0).getShareCode());
        assertEquals("Lib", items.get(0).getName());
    }

    @Test
    void searchReturnsItems() {
        server.expect(requestTo("http://p/index115/search?q=foo&page=1&per_page=20"))
                .andRespond(withSuccess("{\"code\":200,\"data\":{\"total\":1,\"items\":[{\"FileID\":\"f1\",\"ShareCode\":\"sw1\",\"ReceiveCode\":\"6666\",\"Name\":\"a.mkv\",\"IsDir\":false}]}}", MediaType.APPLICATION_JSON));
        var data = client.search(site, "foo", 1, 20);
        assertEquals(1, data.getTotal());
        assertEquals("f1", data.getItems().get(0).getFileId());
    }

    @Test
    void resolveLinkReturnsUrl() {
        server.expect(requestTo("http://p/index115/link"))
                .andExpect(jsonPath("$.share_code").value("sw1"))
                .andExpect(jsonPath("$.file_id").value("f1"))
                .andRespond(withSuccess("{\"code\":200,\"data\":{\"url\":\"http://play/x\",\"expired_in\":600}}", MediaType.APPLICATION_JSON));
        assertEquals("http://play/x", client.resolveLink(site, "CK", "sw1", "6666", "f1"));
    }

    private Site site() {
        Site s = new Site();
        s.setUrl("http://p");
        s.setToken("TKN");
        return s;
    }
}
```

- [ ] **Step 2: Run, verify fail** → FAIL (classes missing).

- [ ] **Step 3: Implement DTOs**

`dto/Index115File.java`:
```java
package cn.har01d.alist_tvbox.dto;

import com.fasterxml.jackson.annotation.JsonProperty;
import lombok.Data;

@Data
public class Index115File {
    @JsonProperty("FileID") private String fileId;
    @JsonProperty("ShareCode") private String shareCode;
    @JsonProperty("ReceiveCode") private String receiveCode;
    @JsonProperty("Name") private String name;
    @JsonProperty("Path") private String path;
    @JsonProperty("Size") private long size;
    @JsonProperty("IsDir") private boolean dir;
    @JsonProperty("Ext") private String ext;
}
```

`dto/Index115Response.java`:
```java
package cn.har01d.alist_tvbox.dto;

import lombok.Data;

@Data
public class Index115Response<T> {
    private int code;
    private String message;
    private T data;
}
```

`dto/Index115SearchData.java`:
```java
package cn.har01d.alist_tvbox.dto;

import lombok.Data;
import java.util.List;

@Data
public class Index115SearchData {
    private int total;
    private List<Index115File> items;
}
```

`dto/Index115LinkData.java`:
```java
package cn.har01d.alist_tvbox.dto;

import com.fasterxml.jackson.annotation.JsonProperty;
import lombok.Data;

@Data
public class Index115LinkData {
    private String url;
    @JsonProperty("expired_in") private long expiredIn;
}
```

`service/Index115Client.java`:
```java
package cn.har01d.alist_tvbox.service;

import cn.har01d.alist_tvbox.dto.Index115File;
import cn.har01d.alist_tvbox.dto.Index115LinkData;
import cn.har01d.alist_tvbox.dto.Index115Response;
import cn.har01d.alist_tvbox.dto.Index115SearchData;
import cn.har01d.alist_tvbox.entity.Site;
import cn.har01d.alist_tvbox.exception.BadRequestException;
import cn.har01d.alist_tvbox.util.Constants;
import lombok.extern.slf4j.Slf4j;
import org.springframework.core.ParameterizedTypeReference;
import org.springframework.http.HttpEntity;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpMethod;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.web.client.RestTemplate;

import java.util.HashMap;
import java.util.List;
import java.util.Map;

@Slf4j
public class Index115Client {
    private final RestTemplate restTemplate;

    public Index115Client(RestTemplate restTemplate) {
        this.restTemplate = restTemplate;
    }

    public List<Index115File> browse(Site site, String shareCode, String receiveCode, String parentId) {
        String url = site.getUrl() + "/index115/browse?share_code={sc}&receive_code={rc}&parent_id={pid}";
        Map<String, String> vars = new HashMap<>();
        vars.put("sc", shareCode == null ? "" : shareCode);
        vars.put("rc", receiveCode == null ? "" : receiveCode);
        vars.put("pid", parentId == null ? "" : parentId);
        return get(site, url, vars, new ParameterizedTypeReference<>() {});
    }

    public Index115SearchData search(Site site, String query, int page, int perPage) {
        String url = site.getUrl() + "/index115/search?q={q}&page={page}&per_page={pp}";
        Map<String, String> vars = new HashMap<>();
        vars.put("q", query);
        vars.put("page", String.valueOf(page));
        vars.put("pp", String.valueOf(perPage));
        return get(site, url, vars, new ParameterizedTypeReference<>() {});
    }

    public String resolveLink(Site site, String cookie, String shareCode, String receiveCode, String fileId) {
        String url = site.getUrl() + "/index115/link";
        HttpHeaders h = headers(site);
        h.setContentType(MediaType.APPLICATION_JSON);
        Map<String, Object> body = new HashMap<>();
        body.put("cookie", cookie);
        body.put("share_code", shareCode);
        body.put("receive_code", receiveCode);
        body.put("file_id", fileId);
        ResponseEntity<Index115Response<Index115LinkData>> resp = restTemplate.exchange(
                url, HttpMethod.POST, new HttpEntity<>(body, h), new ParameterizedTypeReference<>() {});
        return unwrap(resp).getUrl();
    }

    private <T> T get(Site site, String url, Map<String, String> vars, ParameterizedTypeReference<Index115Response<T>> type) {
        ResponseEntity<Index115Response<T>> resp = restTemplate.exchange(
                url, HttpMethod.GET, new HttpEntity<>(null, headers(site)), type, vars);
        return unwrap(resp);
    }

    private HttpHeaders headers(Site site) {
        HttpHeaders h = new HttpHeaders();
        h.set(HttpHeaders.ACCEPT, Constants.ACCEPT);
        h.set(HttpHeaders.USER_AGENT, Constants.USER_AGENT);
        if (site.getToken() != null && !site.getToken().isBlank()) {
            h.set(HttpHeaders.AUTHORIZATION, site.getToken());
        }
        return h;
    }

    private <T> T unwrap(ResponseEntity<Index115Response<T>> resp) {
        Index115Response<T> r = resp.getBody();
        if (r == null || r.getCode() >= 400) {
            throw new BadRequestException(r == null ? "empty PowerList response" : r.getMessage());
        }
        return r.getData();
    }
}
```

- [ ] **Step 4: Run, verify pass** → PASS.
- [ ] **Step 5: Commit** — `"feat: Index115Client + dtos call PowerList /index115"`.

> **Verify (open item):** PowerList `FileItem` JSON field names are PascalCase (Go struct has no json tags). If actual responses differ, adjust the `@JsonProperty` values on `Index115File`.

---

### Task 3: `Index115TvBoxAdapter`

**Files:** Create `service/Index115TvBoxAdapter.java`; Test `service/Index115TvBoxAdapterTest.java`.

- [ ] **Step 1: Failing test**

```java
package cn.har01d.alist_tvbox.service;

import cn.har01d.alist_tvbox.domain.DriverType;
import cn.har01d.alist_tvbox.dto.Index115File;
import cn.har01d.alist_tvbox.dto.Index115SearchData;
import cn.har01d.alist_tvbox.entity.DriverAccount;
import cn.har01d.alist_tvbox.entity.DriverAccountRepository;
import cn.har01d.alist_tvbox.entity.Site;
import cn.har01d.alist_tvbox.tvbox.MovieDetail;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.*;
import static org.mockito.Mockito.*;

@ExtendWith(MockitoExtension.class)
class Index115TvBoxAdapterTest {
    @Mock Index115Client client;
    @Mock ProxyService proxyService;
    @Mock DriverAccountRepository driverAccountRepository;
    Index115TvBoxAdapter adapter;

    @BeforeEach
    void setup() {
        adapter = new Index115TvBoxAdapter(client, proxyService, driverAccountRepository);
    }

    private Site site() {
        Site s = new Site();
        s.setId(9);
        s.setStorageVersion(1);
        return s;
    }

    @Test
    void listRootBuildsShareCategories() {
        Site s = site();
        when(client.browse(s, "", "", "")).thenReturn(List.of(
                file("", "sw1", "6666", "Lib", true),
                file("", "sw2", "7777", "Films", true)));
        when(proxyService.generatePath(eq(s), anyString())).thenReturn(101, 102);

        var ml = adapter.list(s, "/", 1, 100);

        assertEquals(2, ml.getList().size());
        assertEquals("Lib", ml.getList().get(0).getVod_name());
        assertEquals("9$101$1", ml.getList().get(0).getVod_id());
        verify(proxyService).generatePath(s, Index115PathCodec.shareRoot("sw1", "6666"));
    }

    @Test
    void listShareRootUsesParentZero() {
        Site s = site();
        when(client.browse(s, "sw1", "6666", "0")).thenReturn(List.of(
                file("d1", "sw1", "6666", "Dir", true),
                file("f1", "sw1", "6666", "a.mkv", false)));
        when(proxyService.generatePath(eq(s), anyString())).thenReturn(1, 2);

        var ml = adapter.list(s, Index115PathCodec.shareRoot("sw1", "6666"), 1, 100);

        verify(client).browse(s, "sw1", "6666", "0");
        assertEquals(2, ml.getList().size());
    }

    @Test
    void playResolvesLinkWithMasterPan115Cookie() {
        Site s = site();
        DriverAccount acc = new DriverAccount();
        acc.setCookie("CK");
        when(driverAccountRepository.findByTypeAndMasterTrue(DriverType.PAN115)).thenReturn(Optional.of(acc));
        when(client.resolveLink(s, "CK", "sw1", "6666", "f1")).thenReturn("http://play/x");

        Map<String, Object> result = adapter.play(s, Index115PathCodec.child("sw1", "6666", "f1"));

        assertEquals("http://play/x", result.get("url"));
        assertEquals(DriverType.PAN115, result.get("type"));
        assertEquals(0, result.get("parse"));
    }

    @Test
    void searchMapsItems() {
        Site s = site();
        Index115SearchData data = new Index115SearchData();
        data.setTotal(1);
        data.setItems(List.of(file("f1", "sw1", "6666", "a.mkv", false)));
        when(client.search(s, "foo", 1, 20)).thenReturn(data);
        when(proxyService.generatePath(eq(s), anyString())).thenReturn(5);

        List<MovieDetail> list = adapter.search(s, "foo");

        assertEquals(1, list.size());
        assertEquals("a.mkv", list.get(0).getVod_name());
        assertEquals("9$5$1", list.get(0).getVod_id());
    }

    private Index115File file(String fileId, String sc, String rc, String name, boolean dir) {
        Index115File f = new Index115File();
        f.setFileId(fileId);
        f.setShareCode(sc);
        f.setReceiveCode(rc);
        f.setName(name);
        f.setDir(dir);
        return f;
    }
}
```

- [ ] **Step 2: Run, verify fail** → FAIL (class missing).

- [ ] **Step 3: Implement**

```java
package cn.har01d.alist_tvbox.service;

import cn.har01d.alist_tvbox.domain.DriverType;
import cn.har01d.alist_tvbox.dto.Index115File;
import cn.har01d.alist_tvbox.entity.DriverAccount;
import cn.har01d.alist_tvbox.entity.DriverAccountRepository;
import cn.har01d.alist_tvbox.entity.Site;
import cn.har01d.alist_tvbox.exception.BadRequestException;
import cn.har01d.alist_tvbox.tvbox.MovieDetail;
import cn.har01d.alist_tvbox.tvbox.MovieList;
import cn.har01d.alist_tvbox.util.Constants;
import lombok.extern.slf4j.Slf4j;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Optional;

/** Version-1 site TVBox backend: browse/search/play against PowerList /index115. */
@Slf4j
public class Index115TvBoxAdapter {
    private static final int PER_PAGE = 20;

    private final Index115Client client;
    private final ProxyService proxyService;
    private final DriverAccountRepository driverAccountRepository;

    public Index115TvBoxAdapter(Index115Client client, ProxyService proxyService, DriverAccountRepository driverAccountRepository) {
        this.client = client;
        this.proxyService = proxyService;
        this.driverAccountRepository = driverAccountRepository;
    }

    public MovieList list(Site site, String path, int page, int size) {
        String[] ref = Index115PathCodec.decode(path);
        List<Index115File> items;
        if (ref == null) {
            items = client.browse(site, "", "", "");
        } else {
            String parentId = ref[2] == null ? "0" : ref[2];
            items = client.browse(site, ref[0], ref[1], parentId);
        }

        MovieList result = new MovieList();
        List<MovieDetail> list = new ArrayList<>();
        for (Index115File f : items) {
            String childPath = (ref == null)
                    ? Index115PathCodec.shareRoot(f.getShareCode(), f.getReceiveCode())
                    : Index115PathCodec.child(ref[0], ref[1], f.getFileId());
            int pid = proxyService.generatePath(site, childPath);
            MovieDetail md = new MovieDetail();
            md.setVod_id(site.getId() + "$" + pid + "$1");
            md.setVod_name(f.getName());
            md.setVod_tag(f.isDir() ? Constants.FOLDER : Constants.FILE);
            md.setVod_pic(Constants.ALIST_PIC);
            list.add(md);
        }
        result.setList(list);
        result.setPage(page);
        result.setTotal(list.size());
        result.setLimit(list.size());
        result.setPagecount(1);
        return result;
    }

    public Map<String, Object> play(Site site, String path) {
        String[] ref = Index115PathCodec.decode(path);
        if (ref == null || ref[2] == null) {
            throw new BadRequestException("无效的115索引播放路径: " + path);
        }
        String cookie = driverAccountRepository.findByTypeAndMasterTrue(DriverType.PAN115)
                .map(DriverAccount::getCookie).orElse("");
        String url = client.resolveLink(site, cookie, ref[0], ref[1], ref[2]);
        return Map.of(
                "parse", 0,
                "playUrl", "",
                "url", url,
                "type", DriverType.PAN115,
                "header", Map.of("User-Agent", Constants.USER_AGENT, "Referer", "https://115.com/"));
    }

    public List<MovieDetail> search(Site site, String keyword) {
        var data = client.search(site, keyword, 1, PER_PAGE);
        List<MovieDetail> list = new ArrayList<>();
        if (data == null || data.getItems() == null) {
            return list;
        }
        for (Index115File f : data.getItems()) {
            if (f.isDir()) {
                continue;
            }
            int pid = proxyService.generatePath(site, Index115PathCodec.child(f.getShareCode(), f.getReceiveCode(), f.getFileId()));
            MovieDetail md = new MovieDetail();
            md.setVod_id(site.getId() + "$" + pid + "$1");
            md.setVod_name(f.getName());
            md.setVod_tag(Constants.FILE);
            md.setVod_pic(Constants.ALIST_PIC);
            list.add(md);
        }
        return list;
    }
}
```

> If `Constants.ALIST_PIC` / `Constants.FILE` / `Constants.FOLDER` names differ, use the actual constant names (they exist — `FOLDER`/`FILE` are imported in `TvBoxService` from `Constants`).

- [ ] **Step 4: Run, verify pass** → PASS.
- [ ] **Step 5: Commit** — `"feat: Index115TvBoxAdapter threads identity for version-1 browse/search/play"`.

---

### Task 4: `TvBoxService` version-1 branches + beans

**Files:** Modify `service/TvBoxService.java`; Modify `service/Index115Config.java`.

- [ ] **Step 1: Add adapter bean in `Index115Config.java`**

Add inside the class (reuse the existing `index115RestTemplate` bean):

```java
    @Bean
    public cn.har01d.alist_tvbox.service.Index115Client index115Client(RestTemplate index115RestTemplate) {
        return new cn.har01d.alist_tvbox.service.Index115Client(index115RestTemplate);
    }

    @Bean
    public cn.har01d.alist_tvbox.service.Index115TvBoxAdapter index115TvBoxAdapter(
            cn.har01d.alist_tvbox.service.Index115Client index115Client,
            cn.har01d.alist_tvbox.service.ProxyService proxyService,
            cn.har01d.alist_tvbox.entity.DriverAccountRepository driverAccountRepository) {
        return new cn.har01d.alist_tvbox.service.Index115TvBoxAdapter(index115Client, proxyService, driverAccountRepository);
    }
```

- [ ] **Step 2: Inject adapter into `TvBoxService`**

Add a field near the other `private final` fields (e.g. after `private final ProxyService proxyService;`):

```java
    private final Index115TvBoxAdapter index115Adapter;
```

Add `Index115TvBoxAdapter index115Adapter` to the `TvBoxService(...)` constructor parameter list and `this.index115Adapter = index115Adapter;` in the body. Add `import cn.har01d.alist_tvbox.service.Index115TvBoxAdapter;` is unnecessary (same package).

- [ ] **Step 3: Branch `getMovieList`**

In `getMovieList`, immediately after the line `Site site = getSite(tid);`, insert:

```java
        if (site.getStorageVersion() != null && site.getStorageVersion() == 1) {
            return index115Adapter.list(site, path, page, size);
        }
```

- [ ] **Step 4: Branch `getPlayUrl`**

In `getPlayUrl(Integer siteId, String path, boolean getSub, String client, String type)`, immediately after `Site site = siteService.getById(siteId);`, insert:

```java
        if (site.getStorageVersion() != null && site.getStorageVersion() == 1) {
            return index115Adapter.play(site, path);
        }
```

- [ ] **Step 5: Branch `searchByApi`**

At the top of `searchByApi(Site site, String ac, String keyword)`, insert:

```java
        if (site.getStorageVersion() != null && site.getStorageVersion() == 1) {
            return index115Adapter.search(site, keyword);
        }
```

- [ ] **Step 6: Build + run index115 tests**

Run: `./mvnw -q -DskipTests compile && ./mvnw -Dtest='Index115*' test`
Expected: compiles; all index115 tests PASS.

- [ ] **Step 7: Commit** — `"feat: TvBoxService routes version-1 sites to Index115TvBoxAdapter"`.

---

### Task 5: Seed the `index115` site

**Files:** Create `service/Index115SiteSeed.java`.

- [ ] **Step 1: Implement the seed**

```java
package cn.har01d.alist_tvbox.service;

import cn.har01d.alist_tvbox.entity.Setting;
import cn.har01d.alist_tvbox.entity.SettingRepository;
import cn.har01d.alist_tvbox.entity.Site;
import cn.har01d.alist_tvbox.entity.SiteRepository;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.boot.ApplicationArguments;
import org.springframework.boot.ApplicationRunner;
import org.springframework.stereotype.Component;

@Slf4j
@Component
public class Index115SiteSeed implements ApplicationRunner {
    private static final String NAME = "index115";
    private static final String TOKEN_SETTING = "alist_token";

    private final SiteRepository siteRepository;
    private final SettingRepository settingRepository;

    public Index115SiteSeed(SiteRepository siteRepository, SettingRepository settingRepository) {
        this.siteRepository = siteRepository;
        this.settingRepository = settingRepository;
    }

    @Override
    public void run(ApplicationArguments args) {
        if (siteRepository.findAll().stream().anyMatch(s -> NAME.equals(s.getName()))) {
            return;
        }
        String token = settingRepository.findById(TOKEN_SETTING).map(Setting::getValue).orElse("");
        Site site = new Site();
        site.setName(NAME);
        site.setUrl("http://localhost");
        site.setSearchable(true);
        site.setStorageVersion(1);
        if (StringUtils.isNotBlank(token)) {
            site.setToken(token);
        }
        siteRepository.save(site);
        log.info("seeded index115 site (version 1, http://localhost)");
    }
}
```

> `SiteRepository.findAll()` exists (JPA). If `SiteRepository` is in package `entity`, the import above is correct.

- [ ] **Step 2: Build** — `./mvnw -q -DskipTests compile` → OK.
- [ ] **Step 3: Commit** — `"feat: seed index115 version-1 site on startup"`.

---

### Task 6: Manual smoke

- [ ] **Step 1:** Build the app: `./mvnw -q -DskipTests package`.
- [ ] **Step 2:** Run alist-tvbox with a PowerList instance serving `/index115` on `http://localhost`; ensure a master Pan115 account is configured.
- [ ] **Step 3:** In TVBox, load the alist-tvbox subscription → the `index115` category appears; browse → shares → folders → files; search returns hits; play resolves a 115 URL.
- [ ] **Step 4:** If `http://localhost` is wrong for the deployment, edit the `index115` site URL/token via the alist-tvbox UI.

---

## Self-review

- **Spec coverage:** Index115Client (Task 2), version-1 branches in getMovieList/getPlayUrl/searchByApi (Task 4), identity threading via proxyService + codec (Tasks 1, 3), Pan115 cookie for play (Task 3), site seed with alist token (Task 5), root/category auto-inclusion (getCategoryList already iterates sites — covered by Task 4 branch on path="/"). All spec sections covered.
- **Placeholders:** none — every code step is complete. The two `Verify` notes are explicit checkpoints for runtime facts (JSON field casing, constant names), not TBDs.
- **Type consistency:** `Index115Client.browse/search/resolveLink`, `Index115TvBoxAdapter.list/play/search`, `Index115PathCodec.shareRoot/child/decode` signatures match across tests, impl, and the TvBoxService branches.
