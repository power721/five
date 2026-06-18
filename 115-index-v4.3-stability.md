
# 115 Media Index System V4.3 - Stability Design (Production Grade)

## 🎯 Goal
Build a **high-stability 115 index ingestion system** for:
- 2.6M+ files
- long-running crawler (24h~30d)
- proxy failure tolerance
- 115 share/code instability handling
- resumable + incremental indexing

---

# 🧠 1. Stability Architecture

```
            ┌──────────────────────┐
            │  Share Registry DB   │
            └─────────┬────────────┘
                      │
        ┌─────────────▼─────────────┐
        │  Crawl Scheduler          │
        │  (recovery + retry queue) │
        └─────────────┬─────────────┘
                      │
        ┌─────────────▼─────────────┐
        │ Proxy Manager             │
        │ (health + rotation)       │
        └─────────────┬─────────────┘
                      │
        ┌─────────────▼─────────────┐
        │ BFS Crawler Engine        │
        │ (checkpoint + resume)     │
        └─────────────┬─────────────┘
                      │
        ┌─────────────▼─────────────┐
        │ Batch Writer              │
        │ SQLite + Bleve           │
        └───────────────────────────┘
```

---

# 🧱 2. Core Stability Goals

## ✔ Must guarantee

- crawler can restart from crash
- share/code changes handled safely
- proxy failure never stops system
- duplicate crawling avoided
- partial 115 failures tolerated

---

# 🧾 3. Share Registry (Critical Upgrade)

## 3.1 share table (final version)

```sql
CREATE TABLE IF NOT EXISTS share (
    id INTEGER PRIMARY KEY AUTOINCREMENT,

    share_id TEXT NOT NULL,
    code TEXT NOT NULL,

    root_folder_id TEXT,
    mount_path TEXT,

    status TEXT DEFAULT 'ACTIVE',

    last_crawled_at INTEGER,
    last_error TEXT,

    version INTEGER DEFAULT 0,

    UNIQUE(share_id, code)
);
```

---

## 3.2 Share lifecycle

```
ACTIVE → OK crawling
DEAD → invalid / 115 blocked
STALE → code changed / retry needed
QUARANTINE → repeated failure
```

---

# 🔁 4. Crawl Scheduler (核心新增)

## 4.1 responsibilities

- restart failed shares
- detect stale shares
- retry backoff
- incremental crawling

---

## 4.2 scheduler logic

```
loop every 5 min:
    load ACTIVE shares
    load STALE shares (retry)
    push into crawl queue
```

---

## 4.3 Go pseudo code

```go
func Scheduler() {
    for {
        shares := LoadActiveAndStaleShares()

        for _, s := range shares {
            enqueue(Task{
                ShareID: s.ShareID,
                Code: s.Code,
                CID: s.RootFolder,
            })
        }

        time.Sleep(5 * time.Minute)
    }
}
```

---

# 🌐 5. Proxy Manager (Stability Core)
## 5.1 get proxy ip
GET https://share.proxy.qg.net/get?key=<您的key信息>&<其他输入参数>

{
    "code": "SUCCESS",
    "data": [
        {
            "proxy_ip": "123.54.55.24",
            "server": "123.54.55.24:59419",
            "area": "河南省商丘市",
            "isp": "电信",
            "deadline": "2023-02-25 15:38:36"
        }
    ],
    "request_id": "83158ebe-be6c-40f7-a158-688741083edc"
}

## 5.2 auto rotation policy

- failure → switch proxy
- 3 failures → BLOCK
- cooldown → RECOVER

---

# 🧭 6. BFS Crawler (Resumable)

## 6.1 checkpoint system (CRITICAL)

```sql
CREATE TABLE crawl_checkpoint (
    share_id TEXT PRIMARY KEY,
    cid TEXT,
    queue_blob BLOB,
    updated_at INTEGER
);
```

---

## 6.2 resume flow

```
startup:
    load checkpoint
    restore queue
    continue BFS
```

---

## 6.3 BFS logic (stable version)

```go
visited := map[string]bool{}
queue := loadCheckpoint()

for len(queue) > 0 {

    task := queue.pop()

    if visited[task.CID] {
        continue
    }

    visited[task.CID] = true

    nodes := fetch115(task)

    if error:
        retryQueue.push(task)
        continue

    for node in nodes {

        saveToDB(node)

        if node.isDir {
            queue.push(node)
        }
    }

    saveCheckpoint(queue)
}
```

---

# ⚠️ 7. 115 Failure Handling (Key Stability)

## 7.1 failure types

| Type | Action |
|------|--------|
| timeout | retry |
| 403 | proxy rotate |
| empty data | retry + backoff |
| invalid code | mark DEAD |

---

## 7.2 adaptive retry

```
retry delay = base * 2^fail_count
max retry = 5
```

---

# 🧱 8. Incremental Crawling (IMPORTANT)

## 8.1 idea

Do NOT full crawl every time.

---

## 8.2 strategy

```
if share unchanged:
    skip
else:
    crawl delta only
```

---

## 8.3 detection

- compare root CID
- compare file counts
- checksum sampling

---

# 💾 9. Batch Writer Stability

## 9.1 backpressure queue

```
crawler → channel(buffer 1000) → writer
```

---

## 9.2 flush policy

- 200 records OR 2 seconds

---

## 9.3 crash safety

- WAL mode
- idempotent insert (file_id UNIQUE)

---

# 🔍 10. Dedup Strategy (Critical)

## rule:

```
file_id is global identity
```

so:

- ignore duplicates
- allow re-insert safely

---

# 📊 11. Observability (NEW)

## metrics

- crawl speed
- proxy health
- share failure rate
- queue size
- retry count

---

## logs

```
[INFO] share started
[WARN] proxy failed
[ERROR] 115 blocked
```

---

# 🧠 12. Share State Machine

```
ACTIVE
  ↓ failure
STALE
  ↓ repeated failure
QUARANTINE
  ↓ manual review
DEAD
```

---

# 🚀 13. System Guarantees

✔ crash safe crawler  
✔ resumable BFS  
✔ proxy failover  
✔ share lifecycle management  
✔ idempotent ingestion  
✔ stable 7-day crawling  

---

# 📌 14. Final Design Principle

> Stability > Speed > Features

This version prioritizes:

- correctness
- recoverability
- long running stability

over raw throughput.

---

# 🔥 15. Future Upgrade (V5 Ready)

Prepared hooks for:

- metadata layer
- TMDB pipeline
- media classification
- episode reconstruction

---
