# 115 Index Download (alist-tvbox) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a manual `POST /api/index115/update` endpoint in alist-tvbox that fetches `115.version.txt`, skips if unchanged, otherwise mounts the published share as `Pan115Share`, downloads `115.index.zip` via AList (driver auto-转存 into `/alist-tvbox-temp` + auto-delete), and atomically extracts it to `/data/index115`.

**Architecture:** Thin `Index115Controller` → `Index115Service` orchestrates version-fetch/parse/skip, task lifecycle, download, and extract+swap. A `Index115Downloader` seam isolates the AList integration (mocked in service tests). `Pan115Share` handles transfer+cleanup internally on link resolution.

**Tech Stack:** Java 21, Spring Boot, JPA, Mockito + MockRestServiceServer, JUnit 5. Project root `/home/user/workspace/alist-tvbox`, base package `cn.har01d.alist_tvbox`.

**Spec:** `docs/superpowers/specs/2026-06-21-115-index-publish-design.md` (in the `five` repo).

**Key existing APIs (verified):**
- `TaskService`: `addIndexTask`/`addValidateIndexTask` pattern; `startTask(Integer)`, `completeTask(Integer,String,String)`, `failTask(Integer,String)`, `isCancelled(Integer)`, `isTaskRunning(TaskType)`.
- `AListLocalService.saveStorage(Storage)` inserts a storage row; `ShareService.enableStorage(Integer id, String token)` enables it in the running AList.
- `storage.Pan115Share(Share)` builds the 115-Share mount; `storage.Pan115` is the user's own 115.
- `Utils.getDataPath(String...)` → `/data/...` in container.
- `RestTemplate` injected via `RestTemplateBuilder` (see `IndexService`).

---

## File structure

- Modify `src/main/java/cn/har01d/alist_tvbox/domain/TaskType.java` — add `INDEX115`.
- Modify `src/main/java/cn/har01d/alist_tvbox/service/TaskService.java` — add `addIndex115Task()`.
- Create `src/main/java/cn/har01d/alist_tvbox/dto/Index115ShareRef.java` — record.
- Create `src/main/java/cn/har01d/alist_tvbox/service/Index115VersionClient.java` — fetch + parse.
- Create `src/main/java/cn/har01d/alist_tvbox/service/Index115Extractor.java` — extract + atomic swap.
- Create `src/main/java/cn/har01d/alist_tvbox/service/Index115Downloader.java` — interface.
- Create `src/main/java/cn/har01d/alist_tvbox/service/Index115Service.java` — orchestration.
- Create `src/main/java/cn/har01d/alist_tvbox/service/Index115Config.java` — `@Bean` wiring.
- Create `src/main/java/cn/har01d/alist_tvbox/service/AListIndex115Downloader.java` — AList impl.
- Create `src/main/java/cn/har01d/alist_tvbox/web/Index115Controller.java` — endpoint.
- Tests under `src/test/java/cn/har01d/alist_tvbox/service/`.

---

### Task 1: TaskType + task factory

**Files:**
- Modify: `domain/TaskType.java`
- Modify: `service/TaskService.java` (mirror `addValidateIndexTask`)

- [ ] **Step 1: Add enum value**

In `TaskType.java`, append `INDEX115` to the enum (after `DOWNLOAD`, keeping the trailing semicolon valid).

- [ ] **Step 2: Add factory method**

In `TaskService.java`, next to `addValidateIndexTask()`, add:

```java
public Task addIndex115Task() {
    Task task = new Task();
    task.setType(TaskType.INDEX115);
    task.setName("更新115索引");
    task.setCreatedTime(Instant.now());
    return taskRepository.save(task);
}
```

- [ ] **Step 3: Build**

Run: `./mvnw -q -DskipTests compile`
Expected: compiles.

- [ ] **Step 4: Commit**

```bash
git add src/main/java/cn/har01d/alist_tvbox/domain/TaskType.java src/main/java/cn/har01d/alist_tvbox/service/TaskService.java
git commit -m "feat: add INDEX115 task type"
```

---

### Task 2: Version client (fetch + parse)

**Files:**
- Create: `dto/Index115ShareRef.java`
- Create: `service/Index115VersionClient.java`
- Test: `src/test/java/cn/har01d/alist_tvbox/service/Index115VersionClientTest.java`

- [ ] **Step 1: Write failing test**

```java
package cn.har01d.alist_tvbox.service;

import cn.har01d.alist_tvbox.dto.Index115ShareRef;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.http.MediaType;
import org.springframework.test.web.client.MockRestServiceServer;
import org.springframework.web.client.RestTemplate;

import static org.junit.jupiter.api.Assertions.*;
import static org.springframework.test.web.client.match.MockRestRequestMatchers.requestTo;
import static org.springframework.test.web.client.response.MockRestResponseCreators.withSuccess;

class Index115VersionClientTest {
    private RestTemplate restTemplate;
    private Index115VersionClient client;
    private MockRestServiceServer server;

    @BeforeEach
    void setup() {
        restTemplate = new RestTemplate();
        client = new Index115VersionClient(restTemplate, "https://example.test/115.version.txt");
        server = MockRestServiceServer.createServer(restTemplate);
    }

    @Test
    void fetchParsesShareRef() {
        server.expect(requestTo("https://example.test/115.version.txt"))
                .andRespond(withSuccess("swf01d43zby:6666\n", MediaType.TEXT_PLAIN));
        Index115ShareRef ref = client.fetch();
        server.verify();
        assertEquals("swf01d43zby", ref.shareCode());
        assertEquals("6666", ref.receiveCode());
    }

    @Test
    void parseRejectsMalformed() {
        assertNull(Index115VersionClient.parse(null));
        assertNull(Index115VersionClient.parse("nope"));
        assertNull(Index115VersionClient.parse(":6666"));
        assertNull(Index115VersionClient.parse("sw1:"));
    }
}
```

- [ ] **Step 2: Run, verify fail**

Run: `./mvnw -q -Dtest=Index115VersionClientTest test`
Expected: FAIL — classes missing.

- [ ] **Step 3: Implement**

`dto/Index115ShareRef.java`:

```java
package cn.har01d.alist_tvbox.dto;

public record Index115ShareRef(String shareCode, String receiveCode) {}
```

`service/Index115VersionClient.java`:

```java
package cn.har01d.alist_tvbox.service;

import cn.har01d.alist_tvbox.dto.Index115ShareRef;
import lombok.extern.slf4j.Slf4j;
import org.springframework.web.client.RestTemplate;

@Slf4j
public class Index115VersionClient {
    private final RestTemplate restTemplate;
    private final String url;

    public Index115VersionClient(RestTemplate restTemplate, String url) {
        this.restTemplate = restTemplate;
        this.url = url;
    }

    public Index115ShareRef fetch() {
        try {
            return parse(restTemplate.getForObject(url, String.class));
        } catch (Exception e) {
            log.warn("fetch 115.version.txt failed", e);
            return null;
        }
    }

    public static Index115ShareRef parse(String text) {
        if (text == null) {
            return null;
        }
        String line = text.trim();
        int colon = line.indexOf(':');
        if (colon <= 0) {
            return null;
        }
        String shareCode = line.substring(0, colon).trim();
        String receiveCode = line.substring(colon + 1).trim();
        if (shareCode.isEmpty() || receiveCode.isEmpty()) {
            return null;
        }
        return new Index115ShareRef(shareCode, receiveCode);
    }
}
```

- [ ] **Step 4: Run, verify pass**

Run: `./mvnw -q -Dtest=Index115VersionClientTest test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/main/java/cn/har01d/alist_tvbox/dto/Index115ShareRef.java \
        src/main/java/cn/har01d/alist_tvbox/service/Index115VersionClient.java \
        src/test/java/cn/har01d/alist_tvbox/service/Index115VersionClientTest.java
git commit -m "feat: Index115VersionClient fetches and parses 115.version.txt"
```

---

### Task 3: Extractor (unzip + atomic swap)

**Files:**
- Create: `service/Index115Extractor.java`
- Test: `src/test/java/cn/har01d/alist_tvbox/service/Index115ExtractorTest.java`

- [ ] **Step 1: Write failing test**

```java
package cn.har01d.alist_tvbox.service;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.zip.ZipEntry;
import java.util.zip.ZipOutputStream;

import static org.junit.jupiter.api.Assertions.*;

class Index115ExtractorTest {
    @TempDir
    Path temp;

    private Path makeZip(String... entries) throws IOException {
        Path zip = temp.resolve("pkg.zip");
        try (ZipOutputStream z = new ZipOutputStream(Files.newOutputStream(zip))) {
            for (String e : entries) {
                z.putNextEntry(new ZipEntry(e));
                z.write(new byte[]{1});
                z.closeEntry();
            }
        }
        return zip;
    }

    @Test
    void extractsAndSwaps() throws IOException {
        Path zip = makeZip("index.db", "bleve/x");
        Path dir = temp.resolve("index115");
        Files.createDirectories(dir);
        Files.writeString(dir.resolve("stale"), "old");

        new Index115Extractor().extractAndSwap(zip, dir);

        assertTrue(Files.exists(dir.resolve("index.db")));
        assertTrue(Files.exists(dir.resolve("bleve").resolve("x")));
        assertFalse(Files.exists(dir.resolve("stale")), "old contents replaced");
    }

    @Test
    void swapsWhenTargetMissing() throws IOException {
        Path zip = makeZip("index.db");
        Path dir = temp.resolve("index115");

        new Index115Extractor().extractAndSwap(zip, dir);

        assertTrue(Files.exists(dir.resolve("index.db")));
    }
}
```

- [ ] **Step 2: Run, verify fail**

Run: `./mvnw -q -Dtest=Index115ExtractorTest test`
Expected: FAIL — `Index115Extractor` missing.

- [ ] **Step 3: Implement**

`service/Index115Extractor.java`:

```java
package cn.har01d.alist_tvbox.service;

import lombok.extern.slf4j.Slf4j;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.Enumeration;
import java.util.stream.Stream;
import java.util.zip.ZipEntry;
import java.util.zip.ZipFile;

@Slf4j
public class Index115Extractor {
    public void extractAndSwap(Path zip, Path dir) throws IOException {
        Path parent = dir.getParent();
        String name = dir.getFileName().toString();
        Path next = parent.resolve(name + ".new");
        Path old = parent.resolve(name + ".old");

        deleteRecursively(next);
        Files.createDirectories(next);
        unzipTo(zip, next);

        deleteRecursively(old);
        if (Files.exists(dir)) {
            Files.move(dir, old, StandardCopyOption.ATOMIC_MOVE);
        }
        Files.move(next, dir, StandardCopyOption.ATOMIC_MOVE);
        deleteRecursively(old);
    }

    private void unzipTo(Path zip, Path dest) throws IOException {
        try (ZipFile zf = new ZipFile(zip.toFile())) {
            Enumeration<? extends ZipEntry> entries = zf.entries();
            while (entries.hasMoreElements()) {
                ZipEntry entry = entries.nextElement();
                String n = entry.getName();
                if (n.contains("..") || n.startsWith("/") || n.contains("\\")) {
                    throw new IOException("invalid zip entry: " + n);
                }
                Path target = dest.resolve(n).normalize();
                if (!target.startsWith(dest.normalize())) {
                    throw new IOException("zip traversal: " + n);
                }
                if (entry.isDirectory()) {
                    Files.createDirectories(target);
                    continue;
                }
                Files.createDirectories(target.getParent());
                try (InputStream in = zf.getInputStream(entry);
                     OutputStream out = Files.newOutputStream(target)) {
                    in.transferTo(out);
                }
            }
        }
    }

    private void deleteRecursively(Path p) throws IOException {
        if (!Files.exists(p)) {
            return;
        }
        try (Stream<Path> walk = Files.walk(p)) {
            walk.sorted(java.util.Collections.reverseOrder()).forEach(path -> {
                try { Files.deleteIfExists(path); } catch (IOException ignored) {}
            });
        }
    }
}
```

- [ ] **Step 4: Run, verify pass**

Run: `./mvnw -q -Dtest=Index115ExtractorTest test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/main/java/cn/har01d/alist_tvbox/service/Index115Extractor.java \
        src/test/java/cn/har01d/alist_tvbox/service/Index115ExtractorTest.java
git commit -m "feat: Index115Extractor unzips and atomically swaps /data/index115"
```

---

### Task 4: Downloader seam + service orchestration

**Files:**
- Create: `service/Index115Downloader.java`
- Create: `service/Index115Service.java`
- Test: `src/test/java/cn/har01d/alist_tvbox/service/Index115ServiceTest.java`

- [ ] **Step 1: Write failing test**

```java
package cn.har01d.alist_tvbox.service;

import cn.har01d.alist_tvbox.domain.TaskType;
import cn.har01d.alist_tvbox.dto.Index115ShareRef;
import cn.har01d.alist_tvbox.entity.Setting;
import cn.har01d.alist_tvbox.entity.Task;
import cn.har01d.alist_tvbox.repository.SettingRepository;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

import java.nio.file.Path;
import java.util.Optional;

import static org.mockito.ArgumentMatchers.*;
import static org.mockito.Mockito.*;

@ExtendWith(MockitoExtension.class)
class Index115ServiceTest {
    @Mock TaskService taskService;
    @Mock SettingRepository settingRepository;
    @Mock Index115VersionClient versionClient;
    @Mock Index115Downloader downloader;
    @Mock Index115Extractor extractor;
    @InjectMocks Index115Service service;

    private Task task;

    @BeforeEach
    void setup() {
        task = new Task();
        task.setId(7);
        when(taskService.addIndex115Task()).thenReturn(task);
        when(taskService.isTaskRunning(TaskType.INDEX115)).thenReturn(false);
    }

    @Test
    void skipsWhenShareCodeUnchanged() {
        when(versionClient.fetch()).thenReturn(new Index115ShareRef("sw1", "6666"));
        when(settingRepository.findById("index115.share_code")).thenReturn(Optional.of(setting("sw1")));

        service.update();

        verify(downloader, never()).download(anyString(), anyString(), any());
        verify(taskService).completeTask(eq(7), contains("已是最新"), any());
    }

    @Test
    void downloadsExtractsAndPersistsWhenChanged() throws Exception {
        when(versionClient.fetch()).thenReturn(new Index115ShareRef("sw2", "7777"));
        when(settingRepository.findById("index115.share_code")).thenReturn(Optional.empty());

        service.update();

        verify(downloader).download(eq("sw2"), eq("7777"), any(Path.class));
        verify(extractor).extractAndSwap(any(Path.class), any(Path.class));
        verify(settingRepository).save(argThat(s -> "sw2".equals(s.getValue())));
        verify(taskService).completeTask(eq(7), contains("sw2"), any());
    }

    @Test
    void failsTaskWhenVersionMalformed() {
        when(versionClient.fetch()).thenReturn(null);
        service.update();
        verify(taskService).failTask(eq(7), anyString());
        verify(downloader, never()).download(anyString(), anyString(), any());
    }

    @Test
    void failsTaskWhenDownloadThrows() throws Exception {
        when(versionClient.fetch()).thenReturn(new Index115ShareRef("sw2", "7777"));
        when(settingRepository.findById("index115.share_code")).thenReturn(Optional.empty());
        doThrow(new RuntimeException("boom")).when(downloader).download(anyString(), anyString(), any());

        service.update();

        verify(extractor, never()).extractAndSwap(any(Path.class), any(Path.class));
        verify(taskService).failTask(eq(7), contains("boom"));
    }

    private Setting setting(String value) {
        Setting s = new Setting();
        s.setName("index115.share_code");
        s.setValue(value);
        return s;
    }
}
```

- [ ] **Step 2: Run, verify fail**

Run: `./mvnw -q -Dtest=Index115ServiceTest test`
Expected: FAIL — `Index115Service`/`Index115Downloader` missing.

- [ ] **Step 3: Implement the seam**

`service/Index115Downloader.java`:

```java
package cn.har01d.alist_tvbox.service;

import java.io.IOException;
import java.nio.file.Path;

/** Downloads 115.index.zip for a published share. Implementations mount Pan115Share,
 *  which auto-转存 into /alist-tvbox-temp and auto-deletes on link resolution. */
public interface Index115Downloader {
    void download(String shareCode, String receiveCode, Path localDest) throws IOException;
}
```

`service/Index115Service.java`:

```java
package cn.har01d.alist_tvbox.service;

import cn.har01d.alist_tvbox.domain.TaskType;
import cn.har01d.alist_tvbox.dto.Index115ShareRef;
import cn.har01d.alist_tvbox.entity.Setting;
import cn.har01d.alist_tvbox.entity.Task;
import cn.har01d.alist_tvbox.exception.BadRequestException;
import cn.har01d.alist_tvbox.repository.SettingRepository;
import cn.har01d.alist_tvbox.util.Utils;
import lombok.extern.slf4j.Slf4j;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;

@Slf4j
public class Index115Service {
    private static final String SHARE_CODE_KEY = "index115.share_code";

    private final TaskService taskService;
    private final SettingRepository settingRepository;
    private final Index115VersionClient versionClient;
    private final Index115Downloader downloader;
    private final Index115Extractor extractor;

    public Index115Service(TaskService taskService,
                           SettingRepository settingRepository,
                           Index115VersionClient versionClient,
                           Index115Downloader downloader,
                           Index115Extractor extractor) {
        this.taskService = taskService;
        this.settingRepository = settingRepository;
        this.versionClient = versionClient;
        this.downloader = downloader;
        this.extractor = extractor;
    }

    public void update() {
        if (taskService.isTaskRunning(TaskType.INDEX115)) {
            throw new BadRequestException("115索引更新任务进行中");
        }
        Task task = taskService.addIndex115Task();
        taskService.startTask(task.getId());
        try {
            Index115ShareRef ref = versionClient.fetch();
            if (ref == null) {
                taskService.failTask(task.getId(), "115.version.txt 解析失败");
                return;
            }
            String last = settingRepository.findById(SHARE_CODE_KEY).map(Setting::getValue).orElse("");
            if (last.equals(ref.shareCode())) {
                taskService.completeTask(task.getId(), "已是最新 " + ref.shareCode(), null);
                return;
            }
            Path zip = Files.createTempFile("index115-", ".zip");
            try {
                downloader.download(ref.shareCode(), ref.receiveCode(), zip);
                extractor.extractAndSwap(zip, Utils.getDataPath("index115"));
                saveShareCode(ref.shareCode());
                taskService.completeTask(task.getId(), "更新到 " + ref.shareCode(), null);
            } finally {
                Files.deleteIfExists(zip);
            }
        } catch (Exception e) {
            log.error("index115 update failed", e);
            taskService.failTask(task.getId(), e.getMessage());
        }
    }

    private void saveShareCode(String shareCode) {
        Setting s = settingRepository.findById(SHARE_CODE_KEY).orElseGet(Setting::new);
        if (s.getName() == null) {
            s.setName(SHARE_CODE_KEY);
        }
        s.setValue(shareCode);
        settingRepository.save(s);
    }
}
```

> **Confirm** `Setting` has no-arg ctor + `setName`/`setValue`. If its `@Id` is `name` and is non-null on persist, the `orElseGet(Setting::new)` + `setName` path covers it. If the entity differs, adjust only the two lines in `saveShareCode`.

- [ ] **Step 4: Run, verify pass**

Run: `./mvnw -q -Dtest=Index115ServiceTest test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/main/java/cn/har01d/alist_tvbox/service/Index115Downloader.java \
        src/main/java/cn/har01d/alist_tvbox/service/Index115Service.java \
        src/test/java/cn/har01d/alist_tvbox/service/Index115ServiceTest.java
git commit -m "feat: Index115Service orchestrates version check, download, swap"
```

---

### Task 5: AList downloader impl + wiring + controller + smoke

**Files:**
- Create: `service/Index115Config.java`
- Create: `service/AListIndex115Downloader.java`
- Create: `web/Index115Controller.java`

> This task touches the bundled AList integration. **Step 1 is a bounded spike** to pin the exact mount-enable + download mechanism before writing the impl, so the code below is not guessed.

- [ ] **Step 1: Spike — pin the AList mechanism**

Read and record concrete answers (file:line) into a scratch note:
1. `storage/Storage.java`: the `Storage(Share, String)` constructor and `getMountPath(...)` — what mount path a `Pan115Share` gets, and its storage `id`.
2. `service/ShareService.java`: how a share storage is enabled — the exact `enableStorage(id, token)` call and **where the admin `token` comes from** (a setting? `AListLocalService.getSetting(...)` key?).
3. `service/IndexService.java` / `service/FileDownloader.java`: the existing **download-file-to-local** pattern (HttpURLConnection/RestTemplate streaming, redirect handling) to reuse.
4. The bundled AList **download URL** form: `http://127.0.0.1:{aListLocalService.getInternalPort()}/d/{mountPath}/115.index.zip`, and whether `/d/` needs the admin token header.
5. Confirm the share root lists `115.index.zip` (or discover the name via `/api/fs/list`).

Deliverable: a 5-line contract (mount path, id, token source, download URL+auth, file name) used in Step 3. If any piece is missing in the codebase, that becomes an explicit sub-step (add a helper) rather than an assumption.

- [ ] **Step 2: Wiring beans**

`service/Index115Config.java`:

```java
package cn.har01d.alist_tvbox.service;

import org.springframework.boot.web.client.RestTemplateBuilder;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.web.client.RestTemplate;

@Configuration
public class Index115Config {
    private static final String VERSION_URL = "https://d.example.com/115.version.txt";

    @Bean
    public RestTemplate index115RestTemplate(RestTemplateBuilder builder) {
        return builder.build();
    }

    @Bean
    public Index115VersionClient index115VersionClient(RestTemplate index115RestTemplate) {
        return new Index115VersionClient(index115RestTemplate, VERSION_URL);
    }

    @Bean
    public Index115Extractor index115Extractor() {
        return new Index115Extractor();
    }

    @Bean
    public Index115Service index115Service(TaskService taskService,
                                           cn.har01d.alist_tvbox.repository.SettingRepository settingRepository,
                                           Index115VersionClient versionClient,
                                           Index115Downloader downloader,
                                           Index115Extractor extractor) {
        return new Index115Service(taskService, settingRepository, versionClient, downloader, extractor);
    }
}
```

- [ ] **Step 3: Implement AList downloader (against the spiked contract)**

`service/AListIndex115Downloader.java` — fill the TODOs from Step 1's contract:

```java
package cn.har01d.alist_tvbox.service;

import cn.har01d.alist_tvbox.entity.Share;
import cn.har01d.alist_tvbox.storage.Pan115Share;
import cn.har01d.alist_tvbox.util.Utils;
import lombok.extern.slf4j.Slf4j;
import org.springframework.web.client.RestTemplate;

import java.io.IOException;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;

@Slf4j
public class AListIndex115Downloader implements Index115Downloader {
    private final AListLocalService aListLocalService;
    private final ShareService shareService;
    private final RestTemplate restTemplate;
    private final String fileName = "115.index.zip";

    public AListIndex115Downloader(AListLocalService aListLocalService,
                                   ShareService shareService,
                                   RestTemplate index115RestTemplate) {
        this.aListLocalService = aListLocalService;
        this.shareService = shareService;
        this.restTemplate = index115RestTemplate;
    }

    @Override
    public void download(String shareCode, String receiveCode, Path localDest) throws IOException {
        Share share = new Share();
        share.setShareId(shareCode);
        share.setPassword(receiveCode);
        // TODO(spike): set folderId / id per Storage(Share) contract from Step 1
        Pan115Share storage = new Pan115Share(share);
        aListLocalService.saveStorage(storage);
        // TODO(spike): resolve the admin token per Step 1 and enable
        String token = resolveAdminToken();
        String enableError = shareService.enableStorage(storage.getId(), token);
        if (enableError != null) {
            throw new IOException("enable storage failed: " + enableError);
        }

        String url = String.format("http://127.0.0.1:%d/d%s/%s",
                aListLocalService.getInternalPort(), storage.getPath(), fileName);
        // TODO(spike): add admin token header if /d/ requires it (Step 1)
        streamToFile(url, localDest);
        log.info("downloaded {} ({} bytes) from share {}", fileName, Files.size(localDest), shareCode);
    }

    private String resolveAdminToken() {
        // TODO(spike): return the bundled AList admin token (Step 1 pin)
        return "";
    }

    private void streamToFile(String url, Path dest) throws IOException {
        HttpURLConnection conn = (HttpURLConnection) new URL(url).openConnection();
        conn.setConnectTimeout(30000);
        conn.setReadTimeout(300000);
        conn.setInstanceFollowRedirects(true);
        try (InputStream in = conn.getInputStream()) {
            Files.copy(in, dest, StandardCopyOption.REPLACE_EXISTING);
        } finally {
            conn.disconnect();
        }
    }
}
```

Register the bean in `Index115Config`:

```java
    @Bean
    public Index115Downloader index115Downloader(AListLocalService aListLocalService,
                                                  ShareService shareService,
                                                  RestTemplate index115RestTemplate) {
        return new AListIndex115Downloader(aListLocalService, shareService, index115RestTemplate);
    }
```

Resolve each `TODO(spike)` using the Step 1 contract; remove the TODOs once filled.

- [ ] **Step 4: Controller**

`web/Index115Controller.java`:

```java
package cn.har01d.alist_tvbox.web;

import cn.har01d.alist_tvbox.service.Index115Service;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

import java.io.IOException;
import java.util.Map;

@RestController
@RequestMapping("/api/index115")
public class Index115Controller {
    private final Index115Service index115Service;

    public Index115Controller(Index115Service index115Service) {
        this.index115Service = index115Service;
    }

    @ResponseStatus(HttpStatus.ACCEPTED)
    @PostMapping("/update")
    public Map<String, String> update() throws IOException {
        index115Service.update();
        return Map.of("status", "accepted");
    }
}
```

> Auth is global via `TokenFilter` (no annotation needed), matching other `/api/*` admin endpoints.

- [ ] **Step 5: Build + unit tests**

Run: `./mvnw -q -DskipTests=false test`
Expected: all tests PASS (Task 2–4 suites).

- [ ] **Step 6: Manual smoke against a live share**

1. Publish `115.index.zip` and set `115.version.txt` to `shareCode:receiveCode`.
2. Start alist-tvbox, ensure a `Pan115`/115 account is configured with `/alist-tvbox-temp`.
3. `curl -X POST -H "X-API-KEY: $KEY" http://localhost:<port>/api/index115/update`.
4. Verify `/data/index115/index.db` + `/data/index115/bleve/` appear; check the task via `/api/tasks`.
5. Call again → task completes "已是最新" without re-downloading.

- [ ] **Step 7: Commit**

```bash
git add src/main/java/cn/har01d/alist_tvbox/service/Index115Config.java \
        src/main/java/cn/har01d/alist_tvbox/service/AListIndex115Downloader.java \
        src/main/java/cn/har01d/alist_tvbox/web/Index115Controller.java
git commit -m "feat: AList-backed Index115 downloader + /api/index115/update endpoint"
```

---

## Self-review

- **Spec coverage:** version fetch/parse/skip (Task 2, 4), Pan115Share mount+download via AList with auto-转存/delete (Task 5), atomic extract to `/data/index115` (Task 3, 4), manual endpoint (Task 5), shareCode persistence (Task 4). All atv-side spec sections covered.
- **Placeholders:** core logic (Tasks 1–4) is fully concrete. Task 5's AList integration has bounded `TODO(spike)` markers that Step 1 resolves before impl — these are explicit, not hand-waved.
- **Type consistency:** `Index115Downloader.download(shareCode, receiveCode, Path)`, `Index115Extractor.extractAndSwap(zip, dir)`, `Index115VersionClient.fetch()`/`parse()` match across interface/impl/service/test.
