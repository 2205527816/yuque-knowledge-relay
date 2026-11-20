package main

import (
	"database/sql"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var statusLabels = map[string]string{
	"conflict":      "远端有改动",
	"imported":      "已导入",
	"classified":    "已分类",
	"pending_class": "待归类",
	"created":       "已入语雀",
	"terms_ready":   "待审核",
	"written":       "已写回",
	"failed":        "失败",
}

var retentionLabels = map[string]string{
	"active":   "进行中",
	"archived": "已归档",
	"trash":    "回收站",
}

var libraryIcons = map[string]string{
	"文史": "doc", "策论": "doc-word", "四方": "doc-file",
	"时评": "chat-news", "浪潮": "chat-hot", "娱乐": "chat-casual", "冗谈": "chat", "奇偶": "chat-mood",
	"数理": "book-science", "编程": "book-tech", "技法": "method", "人物": "users-people",
	"研学": "box-research", "功利": "box-career", "经营": "box-business",
}

// 页面数据：模板只取字段，不求结构对齐 Jinja。
type pageData struct {
	Section             string
	Status              string
	StatusOptions       []statusOption
	HasFailed           bool
	LibraryDescriptions map[string]string
	ProfileHTML         template.HTML
	Title               string
	Msg                 string
	Error               string
	Docs                []Document
	Libraries           []Library
	LibID               int64
	LibIDStr            string
	Q                   string
	Retention           string
	Page                int
	PerPage             int
	Total               int
	TotalPages          int
	Progress            progressSummary
	Batch               string
	Done                bool
	Writing             int
	PrevURL             string
	NextURL             string
	RetentionURLs       map[string]string
	PageSizes           []int
	HasFilter           bool

	// 文档页专用
	Doc         Document
	Library     *Library
	LibraryName string
	HashShort   string
	Terms       []viewTerm
	Preview     template.HTML
	Missing     []string
	DocProgress docProgress
	ScoreItems  []scoreItem
	Conflict    bool
	IconGroups  []iconGroup
	Profile     string
}

type progressSummary struct {
	Total      int
	Done       int
	Processing int
	Pct        int
	NextTitle  string
}

func icon(name, label, class string) template.HTML {
	if label != "" {
		return template.HTML(fmt.Sprintf(`<svg class="%s" role="img" aria-label="%s"><use href="/static/icons.svg#%s"></use></svg>`, class, template.HTMLEscapeString(label), name))
	}
	return template.HTML(fmt.Sprintf(`<svg class="%s" aria-hidden="true"><use href="/static/icons.svg#%s"></use></svg>`, class, name))
}

func libraryIconClass(name, class string) template.HTML {
	id, ok := libraryIcons[name]
	if !ok {
		id = "doc"
	}
	return icon(id, name, class)
}

func libraryIcon(name string) template.HTML {
	id, ok := libraryIcons[name]
	if !ok {
		id = "doc"
	}
	label := name
	if label == "" {
		label = "未分类"
	}
	return icon(id, label, "library-icon")
}

var templateFuncs = template.FuncMap{
	"approvedSeal":   approvedSeal,
	"formatCount":    formatCount,
	"queueCover":     queueCover,
	"rowStatus":      rowStatus,
	"icon":           icon,
	"libraryIcon":    libraryIcon,
	"statusLabel":    func(s string) string { return pick(statusLabels, s) },
	"retentionLabel": func(s string) string { return pick(retentionLabels, s) },
	"joinCN":         func(items []string) string { return strings.Join(items, "、") },
	"libraryStamp":   func(name string) template.HTML { return libraryIconClass(name, "stamp-icon") },
}

// 每个页面单独解析 base+page，避免多个页面都叫 "content" 互相覆盖。
var pages = map[string]*template.Template{}

func loadTemplates() error {
	for _, name := range []string{"index", "doc", "libraries", "icons", "profile"} {
		t, err := template.New("base.html").Funcs(templateFuncs).ParseFiles(
			filepath.Join("templates", "base.html"),
			filepath.Join("templates", name+".html"),
		)
		if err != nil {
			return err
		}
		pages[name] = t
	}
	return nil
}

func pick(m map[string]string, k string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return k
}

func render(w http.ResponseWriter, page string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data.Section = page
	data.LibraryDescriptions = libraryDescriptions
	t, ok := pages[page]
	if !ok {
		http.Error(w, "模板未加载", 500)
		return
	}
	if err := t.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("render %s: %v", page, err)
	}
}

// cacheStatic 让反代/浏览器缓存静态资源；?v= 变化时自然失效。
func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		h.ServeHTTP(w, r)
	})
}

// queueSummary 对应 main.py 的 _queue_rows/_queue_summary：
// 接收 10 → 分类 30 → 抽词 55 → 注释 80 → 写回 100。
func queueSummary(db *sql.DB) progressSummary {
	rows, err := db.Query(`SELECT d.status, d.library_id,
		(SELECT COUNT(*) FROM terms t WHERE t.document_id=d.id),
		(SELECT COUNT(*) FROM terms t WHERE t.document_id=d.id AND TRIM(COALESCE(t.explanation,'')) <> ''),
		d.title
		FROM documents d WHERE d.retention_status='active' ORDER BY d.id`)
	if err != nil {
		return progressSummary{}
	}
	defer rows.Close()
	var sum progressSummary
	totalPct := 0
	for rows.Next() {
		var status, title string
		var libID sql.NullInt64
		var terms, annotated int
		if err := rows.Scan(&status, &libID, &terms, &annotated, &title); err != nil {
			continue
		}
		pct := 10
		switch {
		case status == "written":
			pct = 100
		case annotated > 0:
			pct = 80
		case terms > 0:
			pct = 55
		case libID.Valid:
			pct = 30
		}
		sum.Total++
		totalPct += pct
		if pct == 100 {
			sum.Done++
		} else if sum.NextTitle == "" {
			sum.NextTitle = title
		}
	}
	sum.Processing = sum.Total - sum.Done
	if sum.Total > 0 {
		sum.Pct = (totalPct + sum.Total/2) / sum.Total
	}
	return sum
}

func listURL(q string, libID int64, retention string, perPage, page int) string {
	v := url.Values{}
	if q != "" {
		v.Set("q", q)
	}
	if libID > 0 {
		v.Set("lib_id", strconv.FormatInt(libID, 10))
	}
	if retention != "active" {
		v.Set("retention", retention)
	}
	if perPage != 20 {
		v.Set("per_page", strconv.Itoa(perPage))
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return "/"
	}
	return "/?" + v.Encode()
}

func parsePage(r *http.Request) (libID int64, q, retention string, page, perPage int) {
	q = strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		q = q[:200]
	}
	retention = r.URL.Query().Get("retention")
	if retention != "active" && retention != "archived" && retention != "trash" && retention != "all" {
		retention = "active"
	}
	libID, _ = strconv.ParseInt(r.URL.Query().Get("lib_id"), 10, 64)
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ = strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	return
}

func indexHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		failOverdue(db, cfg)
		libID, q, retention, page, perPage := parsePage(r)
		status := queueStatus(r.URL.Query().Get("status"))
		filter := listFilter{LibID: libID, Retention: retention, Query: q, Status: status, Limit: perPage}
		filteredURL := func(retention string, page int) string {
			return keepQueueStatus(listURL(q, libID, retention, perPage, page), status)
		}
		total, err := countDocuments(db, filter)
		if err != nil {
			log.Printf("count: %v", err)
			http.Error(w, "数据库错误", 500)
			return
		}
		totalPages := (total + perPage - 1) / perPage
		if totalPages < 1 {
			totalPages = 1
		}
		if page > totalPages {
			page = totalPages
		}
		filter.Offset = (page - 1) * perPage
		docs, err := listDocuments(db, filter)
		if err != nil {
			log.Printf("list: %v", err)
			http.Error(w, "数据库错误", 500)
			return
		}
		libs, _ := listLibraries(db)
		retentionURLs := map[string]string{}
		for _, k := range []string{"active", "archived", "trash", "all"} {
			retentionURLs[k] = filteredURL(k, 1)
		}
		data := pageData{
			Title:         "文章队列 · 知识库中转站",
			Status:        status,
			StatusOptions: queueStatuses,
			Msg:           r.URL.Query().Get("msg"),
			Error:         r.URL.Query().Get("error"),
			Docs:          docs,
			Libraries:     libs,
			LibID:         libID,
			Q:             q,
			Retention:     retention,
			Page:          page,
			PerPage:       perPage,
			Total:         total,
			TotalPages:    totalPages,
			Progress:      queueSummary(db),
			Batch:         r.URL.Query().Get("batch"),
			Done:          r.URL.Query().Get("done") != "",
			Writing:       pendingWriteCount(db),
			RetentionURLs: retentionURLs,
			PageSizes:     []int{10, 20, 50, 100},
			HasFilter:     q != "" || libID > 0 || retention != "active" || status != "",
		}
		_ = db.QueryRow("SELECT EXISTS (SELECT 1 FROM documents WHERE retention_status='active' AND status='failed')").Scan(&data.HasFailed)
		if libID > 0 {
			data.LibIDStr = strconv.FormatInt(libID, 10)
		}
		if page > 1 {
			data.PrevURL = filteredURL(retention, page-1)
		}
		if page < totalPages {
			data.NextURL = filteredURL(retention, page+1)
		}
		render(w, "index", data)
	}
}

// migrateCheck 在真实库副本上跑一遍：补列、quick_check、计数对账。
func migrateCheck(path string) error {
	db, err := openDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := initDB(db); err != nil {
		return err
	}
	var integrity string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&integrity); err != nil {
		return err
	}
	counts := map[string]int{}
	for _, t := range []string{"libraries", "documents", "terms", "glossary", "events", "batches"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + t).Scan(&n); err == nil {
			counts[t] = n
		}
	}
	log.Printf("quick_check=%s counts=%v", integrity, counts)
	return nil
}

func main() {
	addr := flag.String("addr", ":8011", "监听地址")
	root := flag.String("root", "/opt/knowledge-relay", "项目根目录")
	checkDB := flag.String("check", "", "只做迁移检查：打开指定数据库、补列、quick_check、输出计数")
	checkYuque := flag.Bool("check-yuque", false, "用当前配置读一次语雀目录，验证通道可用")
	reflowDoc := flag.Int64("reflow-doc", 0, "重排指定文档的正文换行（HTML 导入的旧文章）")
	reflowForce := flag.Bool("reflow-force", false, "配合 -reflow-doc：真正写入（默认只 dry-run）")
	flag.Parse()
	rootDir = *root
	if *reflowDoc > 0 {
		db, err := openDB(dbPath())
		if err != nil {
			log.Fatalf("打开数据库失败: %v", err)
		}
		defer db.Close()
		var body, sourceURL string
		if err := db.QueryRow("SELECT raw_md, COALESCE(source_url,'') FROM documents WHERE id=?", *reflowDoc).Scan(&body, &sourceURL); err != nil {
			log.Fatalf("读文档失败: %v", err)
		}
		if sourceURL == "" {
			log.Fatalf("doc %d 无 source_url，不是网页导入，拒绝重排", *reflowDoc)
		}
		next := reflowParagraphs(body)
		before, after := countLines(body), countLines(next)
		log.Printf("doc %d: %d 行 → %d 行，hash %s → %s", *reflowDoc, before, after, contentHash(body)[:12], contentHash(next)[:12])
		if !*reflowForce {
			log.Printf("dry-run：未写入；确认后加 -reflow-force")
			return
		}
		if _, err := db.Exec("UPDATE documents SET raw_md=? WHERE id=?", next, *reflowDoc); err != nil {
			log.Fatalf("写回失败: %v", err)
		}
		addEvent(db, *reflowDoc, "reflow", map[string]any{"before_lines": before, "after_lines": after})
		log.Printf("doc %d 已写入", *reflowDoc)
		return
	}
	if *checkYuque {
		loadDotEnv()
		c, err := loadConfig()
		if err != nil {
			log.Fatalf("配置失败: %v", err)
		}
		client := newYuqueClients(c)
		if client == nil {
			log.Fatal("未配置语雀凭据")
		}
		if len(c.Libraries) == 0 {
			log.Fatal("config.yaml 未配置 libraries，无法验证通道")
		}
		lib := c.Libraries[0]
		nodes, err := client.GetTOC(lib.Namespace, lib.Slug)
		if err != nil {
			log.Fatalf("语雀读目录失败: %v", err)
		}
		log.Printf("语雀通道 OK，%s 目录 %d 条", lib.Name, len(nodes))
		return
	}
	if *checkDB != "" {
		if err := migrateCheck(*checkDB); err != nil {
			log.Fatalf("检查失败: %v", err)
		}
		return
	}
	loadDotEnv()
	var err error
	cfg, err = loadConfig()
	if err != nil {
		log.Fatalf("读 config.yaml 失败: %v", err)
	}
	db, err := openDB(dbPath())
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()
	if err := initDB(db); err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	if err := syncLibraries(db, cfg.Libraries); err != nil {
		log.Printf("同步库配置失败: %v", err)
	}
	if n := recoverInterruptedJobs(db); n > 0 {
		log.Printf("启动恢复：%d 篇中断任务已标记失败", n)
	}
	if err := loadTemplates(); err != nil {
		log.Fatalf("加载模板失败: %v", err)
	}

	startWriteWorker(db, cfg)
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	}))
	mux.Handle("GET /", indexHandler(db))
	mux.Handle("GET /docs/{id}", docHandler(db))
	mux.Handle("POST /docs/{id}/save", saveHandler(db))
	mux.Handle("POST /docs/{id}/preview", previewHandler(db, cfg))
	mux.Handle("POST /docs/{id}/terms/{termid}/regenerate", regenerateHandler(db, cfg))
	mux.Handle("POST /docs/{id}/terms/add", addTermHandler(db, cfg))
	mux.Handle("POST /libraries/{id}", librarySettingsHandler(db))
	mux.Handle("GET /libraries/{id}/profile", profilePageHandler(db))
	mux.Handle("POST /libraries/{id}/profile", profileSaveHandler(db))
	mux.Handle("POST /docs/{id}/write", writeHandler(db, cfg))
	mux.Handle("POST /docs/{id}/retry", retryHandler(db, cfg))
	mux.Handle("POST /docs/{id}/retention", retentionHandler(db))
	mux.Handle("POST /docs/{id}/library", libraryHandler(db, cfg))
	mux.Handle("POST /docs/batch", batchHandler(db, cfg))
	mux.Handle("POST /upload", uploadHandler(db, cfg))
	mux.Handle("POST /scan", scanHandler(db, cfg))
	mux.Handle("POST /ingest/simpread", simpreadHandler(db, cfg))
	mux.Handle("GET /api/progress", progressHandler(db))
	mux.Handle("GET /libraries", librariesHandler(db))
	mux.Handle("GET /icons", iconsHandler())
	mux.Handle("GET /img", http.HandlerFunc(imgHandler))
	mux.Handle("GET /sw.js", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, "self.registration.unregister();caches.keys().then(function(ks){return Promise.all(ks.map(function(k){return caches.delete(k)}))});")
	}))
	mux.Handle("GET /static/", cacheStatic(http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join(rootDir, "app", "static"))))))

	log.Printf("listening on %s (root=%s)", *addr, rootDir)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func init() {
	// 模板相对路径依赖工作目录 = goapp/
	if _, err := os.Stat("templates"); err != nil {
		if wd, err := os.Getwd(); err == nil {
			log.Printf("cwd=%s", wd)
		}
	}
}

func countLines(s string) int { return len(strings.Split(s, "\n")) }
