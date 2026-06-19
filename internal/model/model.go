package model

type File struct {
	FileID    string
	ShareCode string
	ParentID  string
	Name      string
	Path      string
	Ext       string
	Size      int64
	IsDir     bool
	Depth     int
	SHA1      string
	UpdatedAt int64
	CrawledAt int64
}

type Share struct {
	ShareCode      string
	ReceiveCode    string
	ShareTitle     string
	FileSize       int64
	Status         string
	LastCrawledAt  int64
	LastError      string
	Version        int64
	FailureCount   int
	RetryAfterUnix int64
}

type CrawlTask struct {
	CID   string `json:"cid"`
	Path  string `json:"path"`
	Depth int    `json:"depth"`
}

type Checkpoint struct {
	ShareCode   string
	CID         string
	NextOffset  int
	ActivePath  string
	ActiveDepth int
	Queue       []CrawlTask
	Visited     map[string]bool
	UpdatedAt   int64
}

type IndexEvent struct {
	ID     int64
	FileID string
	Op     string
}

type IndexManifest struct {
	Version   int64
	IndexPath string
	Status    string
	BuiltAt   int64
	FileCount int64
}
