
🚀 V5（Query Layer First Design）

🎯 目标：把 260万索引变成“可用产品”

🧠 1. 架构升级（核心变化）
            ┌──────────────────────────┐
            │   Indexer Service        │
            │ (already done: V4.3)     │
            └────────────┬─────────────┘
                         │ SQLite + Bleve
                         ▼
        ┌────────────────────────────────┐
        │      QUERY LAYER (NEW)        │
        │  - Search Engine              │
        │  - Browse Engine              │
        │  - Facet / Filter Engine      │
        └────────────┬───────────────────┘
                     │
          ┌──────────┴──────────┐
          ▼                     ▼
   WebDAV Adapter        REST API Server
          │                     │
          ▼                     ▼
      AList / TVBox      Web UI / Apps
⚠️ 2. V5重点变化（非常重要）
❌ 不做：
❌ 刮削
❌ TMDB
❌ media识别
❌ work聚类
✔ 只做：

👉 “让 260万文件变得可浏览 + 可搜索 + 可筛选”

🔥 3. Query Layer 设计（核心系统）
🧱 3.1 Search Engine（Bleve增强版）
目标：
秒级搜索（<100ms）
支持前缀 / 模糊
支持 path + name
Index字段设计升级
type SearchDoc struct {
    FileID   string
    ShareID  string

    Name     string
    Path     string

    Ext      string

    // 🚀 关键优化字段
    NameTokens []string
    PathTokens []string
}
Bleve Mapping（升级）
mapping := bleve.NewIndexMapping()

doc := bleve.NewDocumentMapping()

doc.AddFieldMappingsAt("name",
    bleve.NewTextFieldMapping(),
)

doc.AddFieldMappingsAt("path",
    bleve.NewTextFieldMapping(),
)

doc.AddFieldMappingsAt("ext",
    bleve.NewKeywordFieldMapping(),
)

mapping.AddDocumentMapping("file", doc)
Search API
GET /search?q=avatar
GET /search?q=season 1
GET /search?q=1080p remux
Query实现（关键）
func Search(q string) ([]File, error) {

    query := bleve.NewQueryStringQuery(q)

    req := bleve.NewSearchRequest(query)
    req.Size = 50

    res, err := Index.Search(req)
    if err != nil {
        return nil, err
    }

    var out []File

    for _, hit := range res.Hits {
        out = append(out, File{
            FileID: hit.ID,
            Name:   hit.Fields["name"].(string),
            Path:   hit.Fields["path"].(string),
        })
    }

    return out, nil
}
🧭 4. Browse Engine（关键）
目标：

👉 把 SQLite 变成“虚拟文件系统”

API
GET /browse?share_id=xxx&parent_id=xxx
实现
func Browse(shareID string, parentID string) ([]File, error) {

    rows, _ := DB.Query(`
        SELECT file_id, name, path, is_dir
        FROM file
        WHERE share_id=? AND parent_id=?
        ORDER BY is_dir DESC, name ASC
    `, shareID, parentID)

    var res []File

    for rows.Next() {
        var f File
        rows.Scan(&f.FileID, &f.Name, &f.Path, &f.IsDir)
        res = append(res, f)
    }

    return res, nil
}
⚡ 5. 浏览体验优化（核心）
5.1 文件排序策略
目录优先
文件按名称
视频文件优先（未来扩展）
5.2 path 虚拟化
/share/movie/Avatar.mkv
🔎 6. Facet Filter（非常重要）
目标：

👉 260万文件不能只 search，要 filter

支持：
ext
size range
folder depth
share_id
API
GET /filter?ext=mp4
GET /filter?share_id=xxx
SQL实现
SELECT * FROM file
WHERE ext = ?
🌐 7. WebDAV（重点重写）
目标：

👉 让 AList / TVBox 直接用

映射规则
/share_id + file_path
设计
func ListDir(path string) []File {

    return DB.Query(`
        SELECT file_name, file_path, is_dir
        FROM file
        WHERE file_path LIKE ?
    `, path+"%")
}
🚀 8. API Gateway（Query Layer）
REST API
GET /search?q=
GET /browse
GET /filter
GET /file/:id
返回结构（统一）
{
  "file_id": "",
  "name": "",
  "path": "",
  "is_dir": false
}
🧠 9. 性能设计（260万核心）
9.1 SQLite优化
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA temp_store=MEMORY;
9.2 索引优化
CREATE INDEX idx_share_parent ON file(share_id, parent_id);
CREATE INDEX idx_ext ON file(ext);
9.3 query cache（重要）
map[string][]File
⚡ 10. Query Layer 数据流
SQLite (260万)
   ↓
Browse Engine
   ↓
Search Engine (Bleve)
   ↓
Filter Engine
   ↓
API / WebDAV
   ↓
AList / TVBox
🧩 11. 系统定位（非常重要）
V4（你之前做的）：

👉 “数据采集系统”

V5（这一层）：

👉 “可用产品系统”

🔥 V5 核心价值

✔ 260万文件可浏览
✔ 秒级搜索
✔ 可接 TVBox
✔ 可接 AList
✔ 可作为文件系统使用

🚀 12. 下一步升级（可选）

如果你继续往下走，我建议：

V6（媒体体验层）
文件预览
视频识别
剧集UI
poster墙
TMDB（这时候才做）
📌 最关键结论

你现在系统分两阶段：

V4 = Indexing Engine
V5 = Query Engine  ← 你现在要做这个