package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"five/internal/adminhttp"
	"five/internal/api115"
	"five/internal/crawler"
	"five/internal/metrics"
	"five/internal/model"
	"five/internal/proxy"
	"five/internal/scheduler"
	"five/internal/searchindex"
	"five/internal/shares"
	"five/internal/store"
)

func main() {
	var (
		dbPath            = flag.String("db", "data/index.db", "sqlite database path")
		blevePath         = flag.String("bleve", "data/bleve", "bleve root directory")
		mode              = flag.String("mode", "crawl", "crawl or rebuild-index")
		shareCode         = flag.String("share-code", "", "115 share code")
		receiveCode       = flag.String("receive-code", "", "115 receive code")
		shareURL          = flag.String("share-url", "", "115 share URL, e.g. https://115.com/s/<share>?password=<code>")
		sharesFile        = flag.String("shares-file", "115_shares.txt", "shares file path for import-shares mode")
		cookie            = flag.String("cookie", "", "115 cookie header value")
		userAgent         = flag.String("user-agent", "Mozilla/5.0", "http user-agent")
		schedulerInterval = flag.Duration("scheduler-interval", time.Minute, "scheduler polling interval")
		indexInterval     = flag.Duration("index-interval", 30*time.Second, "incremental index polling interval")
		indexBatchSize    = flag.Int("index-batch-size", 100, "incremental index batch size")
		proxyKey          = flag.String("proxy-key", "", "proxy provider key")
		proxyPassword     = flag.String("proxy-password", "", "proxy provider password")
		envFile           = flag.String("env-file", defaultEnvFile, "optional env file path for credentials")
		metricsAddr       = flag.String("metrics-addr", "", "metrics HTTP listen address, e.g. :9090")
		adminAddr         = flag.String("admin-addr", "", "admin HTTP listen address, e.g. :8080")
		backfillDelay     = flag.Duration("backfill-delay", 500*time.Millisecond, "delay between share/snap requests in backfill-share-meta mode")
		outPath           = flag.String("out", "", "output path for export-db mode, e.g. dist/index.db")
	)
	flag.Parse()

	ctx, stopSignals := contextWithSignals(context.Background())
	defer stopSignals()
	s, err := store.Open(ctx, *dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer s.Close()
	cookieStore := store.NewCookieStore(s, "")
	var proxyCfg proxyConfig
	if needsProxy(*mode) {
		proxyCfg, err = resolveProxyConfig(*proxyKey, *proxyPassword, *envFile)
		if err != nil {
			log.Fatal(err)
		}
	}

	switch *mode {
	case "crawl":
		if *shareCode == "" || *receiveCode == "" {
			log.Fatal("crawl mode requires -share-code and -receive-code")
		}
		proxyMgr := proxy.New(proxy.Config{})
		provider := newProxyProvider(proxyCfg)
		validator := &proxy.HTTPValidator{
			UserAgent: *userAgent,
			Cookie:    *cookie,
		}
		client := &api115.Client{
			HTTPClient:  &http.Client{Timeout: 20 * time.Second},
			Cookie:      *cookie,
			CookieStore: cookieStore,
			UserAgent:   *userAgent,
			ProxyPool:   proxyAccess{manager: proxyMgr, provider: provider, validator: validator},
		}
		lister := apiLister{client: client}
		c := crawler.New(lister, s, crawler.Config{PageSize: 100})
		share := model.Share{
			ShareCode:   *shareCode,
			ReceiveCode: *receiveCode,
		}
		if err := c.CrawlShare(ctx, share, time.Now().Unix()); err != nil {
			log.Fatalf("crawl share: %v", err)
		}
	case "register-share":
		var share model.Share
		if *shareURL != "" {
			parsed, err := shares.ParseURL(*shareURL)
			if err != nil {
				log.Fatalf("parse share url: %v", err)
			}
			share = parsed
		} else {
			if *shareCode == "" {
				log.Fatal("register-share mode requires -share-url or -share-code")
			}
			share = model.Share{
				ShareCode:   *shareCode,
				ReceiveCode: *receiveCode,
				Status:      "ACTIVE",
			}
		}
		if err := s.UpsertShare(ctx, share); err != nil {
			log.Fatalf("upsert share: %v", err)
		}
		fmt.Fprintf(os.Stdout, "registered share %s\n", share.ShareCode)
	case "import-shares":
		f, err := os.Open(*sharesFile)
		if err != nil {
			log.Fatalf("open shares file: %v", err)
		}
		defer f.Close()
		parsed, err := shares.Parse(f)
		if err != nil {
			log.Fatalf("parse shares file: %v", err)
		}
		for _, share := range parsed {
			if err := s.UpsertShare(ctx, share); err != nil {
				log.Fatalf("upsert share %s: %v", share.ShareCode, err)
			}
		}
		fmt.Fprintf(os.Stdout, "imported %d shares\n", len(parsed))
	case "backfill-share-meta":
		f, err := os.Open(*sharesFile)
		if err != nil {
			log.Fatalf("open shares file: %v", err)
		}
		defer f.Close()
		parsed, err := shares.Parse(f)
		if err != nil {
			log.Fatalf("parse shares file: %v", err)
		}
		client := &api115.Client{
			HTTPClient:  &http.Client{Timeout: 20 * time.Second},
			Cookie:      *cookie,
			CookieStore: cookieStore,
			UserAgent:   *userAgent,
		}
		// Route through the proxy pool when credentials are available; the
		// share/snap endpoint is easily rate-limited from a single IP. Fall
		// back to a direct request when no proxy is configured.
		if cfg, perr := resolveProxyConfig(*proxyKey, *proxyPassword, *envFile); perr == nil {
			proxyMgr := proxy.New(proxy.Config{})
			provider := newProxyProvider(cfg)
			validator := &proxy.HTTPValidator{UserAgent: *userAgent, Cookie: *cookie}
			client.ProxyPool = proxyAccess{manager: proxyMgr, provider: provider, validator: validator}
		} else {
			log.Printf("event=backfill_direct reason=%q", perr.Error())
		}
		n, err := backfillShareMeta(ctx, client, s, parsed, *backfillDelay, os.Stdout)
		if err != nil {
			log.Fatalf("backfill share meta: %v", err)
		}
		fmt.Fprintf(os.Stdout, "backfilled %d of %d shares\n", n, len(parsed))
	case "validate-share-counts":
		shares, err := s.ListShares(ctx)
		if err != nil {
			log.Fatalf("list shares: %v", err)
		}
		client := &api115.Client{
			HTTPClient:  &http.Client{Timeout: 20 * time.Second},
			Cookie:      *cookie,
			CookieStore: cookieStore,
			UserAgent:   *userAgent,
		}
		if cfg, perr := resolveProxyConfig(*proxyKey, *proxyPassword, *envFile); perr == nil {
			proxyMgr := proxy.New(proxy.Config{})
			provider := newProxyProvider(cfg)
			validator := &proxy.HTTPValidator{UserAgent: *userAgent, Cookie: *cookie}
			client.ProxyPool = proxyAccess{manager: proxyMgr, provider: provider, validator: validator}
		} else {
			log.Printf("event=validate_direct reason=%q", perr.Error())
		}
		if _, err := validateShareCounts(ctx, client, s, shares, os.Stdout); err != nil {
			log.Fatalf("validate share counts: %v", err)
		}
	case "run-scheduler-once":
		proxyMgr := proxy.New(proxy.Config{})
		provider := newProxyProvider(proxyCfg)
		validator := &proxy.HTTPValidator{
			UserAgent: *userAgent,
			Cookie:    *cookie,
		}
		client := &api115.Client{
			HTTPClient:  &http.Client{Timeout: 20 * time.Second},
			Cookie:      *cookie,
			CookieStore: cookieStore,
			UserAgent:   *userAgent,
			ProxyPool:   proxyAccess{manager: proxyMgr, provider: provider, validator: validator},
		}
		lister := apiLister{client: client}
		c := crawler.New(lister, s, crawler.Config{PageSize: 100})
		sched := scheduler.New(s, c, log.Writer())
		if _, err := sched.RunOnce(ctx, time.Now().Unix()); err != nil {
			log.Fatalf("run scheduler once: %v", err)
		}
		fmt.Fprintln(os.Stdout, "scheduler run completed")
	case "daemon":
		reg := metrics.New()
		builder := searchindex.New(*blevePath)
		manifest, ok, err := s.LoadManifest(ctx)
		if err != nil {
			log.Fatalf("load manifest: %v", err)
		}
		if !ok {
			manifest, err = builder.Rebuild(ctx, s, time.Now().Unix(), time.Now().Unix())
			if err != nil {
				log.Fatalf("bootstrap rebuild index: %v", err)
			}
			if err := s.UpdateManifest(ctx, manifest); err != nil {
				log.Fatalf("update manifest: %v", err)
			}
		}
		proxyMgr := proxy.New(proxy.Config{})
		proxyProvider := newProxyProvider(proxyCfg)
		validator := &proxy.HTTPValidator{
			UserAgent: *userAgent,
			Cookie:    *cookie,
		}
		client := &api115.Client{
			HTTPClient:  &http.Client{Timeout: 20 * time.Second},
			Cookie:      *cookie,
			CookieStore: cookieStore,
			UserAgent:   *userAgent,
			ProxyPool:   proxyAccess{manager: proxyMgr, provider: proxyProvider, validator: validator},
		}
		lister := apiLister{client: client}
		c := crawler.New(lister, s, crawler.Config{PageSize: 100})
		sched := scheduler.New(s, c, log.Writer())
		log.Printf(
			"event=daemon_start scheduler_interval=%s index_interval=%s index_batch_size=%d proxy_enabled=%t admin_addr=%q metrics_addr=%q",
			pollInterval(*schedulerInterval, time.Minute),
			pollInterval(*indexInterval, 30*time.Second),
			*indexBatchSize,
			proxyMgr != nil,
			localOnlyListenAddr(*adminAddr),
			*metricsAddr,
		)
		var loops []loopFunc
		loops = append(loops,
			func(ctx context.Context) error {
				reg.IncCounter("daemon_scheduler_loops_total", 1)
				return sched.RunLoop(ctx, pollInterval(*schedulerInterval, time.Minute))
			},
			func(ctx context.Context) error {
				reg.IncCounter("daemon_index_loops_total", 1)
				return builder.RunEventLoop(ctx, manifest.IndexPath, s, *indexBatchSize, pollInterval(*indexInterval, 30*time.Second))
			},
		)
		if *metricsAddr != "" {
			loops = append(loops, func(ctx context.Context) error {
				log.Printf("event=metrics_server_start addr=%q", *metricsAddr)
				server := &http.Server{
					Addr:    *metricsAddr,
					Handler: reg.Handler(),
				}
				errCh := make(chan error, 1)
				go func() {
					errCh <- server.ListenAndServe()
				}()
				select {
				case <-ctx.Done():
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = server.Shutdown(shutdownCtx)
					return nil
				case err := <-errCh:
					if err == http.ErrServerClosed {
						return nil
					}
					return err
				}
			})
		}
		if *adminAddr != "" {
			loops = append(loops, func(ctx context.Context) error {
				addr := localOnlyListenAddr(*adminAddr)
				log.Printf("event=admin_server_start addr=%q", addr)
				server := &http.Server{
					Addr:    addr,
					Handler: adminhttp.New(s, log.Writer()),
				}
				errCh := make(chan error, 1)
				go func() {
					errCh <- server.ListenAndServe()
				}()
				select {
				case <-ctx.Done():
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = server.Shutdown(shutdownCtx)
					return nil
				case err := <-errCh:
					if err == http.ErrServerClosed {
						return nil
					}
					return err
				}
			})
		}
		if err := runDaemon(ctx, loops...); err != nil {
			log.Fatalf("daemon run: %v", err)
		}
	case "rebuild-index":
		builder := searchindex.New(*blevePath)
		manifest, err := builder.Rebuild(ctx, s, time.Now().Unix(), time.Now().Unix())
		if err != nil {
			log.Fatalf("rebuild index: %v", err)
		}
		if err := s.UpdateManifest(ctx, manifest); err != nil {
			log.Fatalf("update manifest: %v", err)
		}
		fmt.Fprintf(os.Stdout, "rebuilt index at %s with %d files\n", manifest.IndexPath, manifest.FileCount)
	case "export-db":
		if *outPath == "" {
			log.Fatal("export-db mode requires -out")
		}
		manifest, ok, err := s.LoadManifest(ctx)
		if err != nil {
			log.Fatalf("load manifest: %v", err)
		}
		var bleveSrc string
		switch {
		case ok && manifest.Status == "READY":
			bleveSrc = manifest.IndexPath
		default:
			bleveSrc = newestBleveIndex(*blevePath)
			if bleveSrc == "" {
				log.Fatal("no READY bleve index; run rebuild-index first")
			}
			log.Printf("warning: no READY manifest; using bleve index %s", bleveSrc)
		}
		tmp, err := os.MkdirTemp("", "five-export-")
		if err != nil {
			log.Fatalf("temp dir: %v", err)
		}
		defer os.RemoveAll(tmp)
		trimmedDB := filepath.Join(tmp, "index.db")
		if err := s.ExportTrimmed(ctx, trimmedDB); err != nil {
			log.Fatalf("export trimmed: %v", err)
		}
		if err := buildPackage(trimmedDB, bleveSrc, *outPath); err != nil {
			log.Fatalf("build package: %v", err)
		}
		fmt.Fprintf(os.Stdout, "packaged index to %s (db trimmed to file+share; bleve from %s)\n", *outPath, bleveSrc)
	default:
		log.Fatalf("unsupported mode %q", *mode)
	}
}

type apiLister struct {
	client *api115.Client
}

type proxyAccess struct {
	manager   *proxy.Manager
	provider  proxy.Fetcher
	validator proxy.Validator
}

func (p proxyAccess) Acquire(ctx context.Context) (api115.ProxyRef, bool) {
	if p.manager == nil {
		return api115.ProxyRef{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return p.manager.Acquire(ctx, p.provider, p.validator)
}

func (p proxyAccess) RecordFailure(id string) {
	if p.manager != nil {
		p.manager.RecordFailure(id)
	}
}

func (p proxyAccess) RecordSuccess(id string) {
	if p.manager != nil {
		p.manager.RecordSuccess(id)
	}
}

func (l apiLister) ListPage(ctx context.Context, share model.Share, cid string, offset, limit int) (crawler.Page, error) {
	resp, err := l.client.List(ctx, api115.ListRequest{
		ShareCode:   share.ShareCode,
		ReceiveCode: share.ReceiveCode,
		CID:         cid,
		Offset:      offset,
		Limit:       limit,
	})
	if err != nil {
		return crawler.Page{}, err
	}
	if !resp.ValidShare() {
		return crawler.Page{}, api115.ErrDeadShare
	}
	nodes := make([]model.File, 0, len(resp.Data.List))
	for _, node := range resp.Data.List {
		filePath := "/" + node.Name
		nodes = append(nodes, node.ToFile(share.ShareCode, cid, filePath, 0, time.Now().Unix()))
	}
	return crawler.Page{
		Nodes:      nodes,
		HasMore:    offset+limit < resp.Data.Count,
		ShareTitle: resp.Data.ShareInfo.ShareTitle,
		FileSize:   resp.Data.ShareInfo.FileSize,
	}, nil
}
