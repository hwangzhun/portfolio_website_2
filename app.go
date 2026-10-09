package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type App struct {
	store                        *Store
	uploadDir                    string
	router                       *http.ServeMux
	static                       http.Handler
	generalLimiter, loginLimiter *rateLimiter
	accessLogCount               atomic.Uint64
	iconMu                       sync.Mutex
	iconCache                    map[string][]byte
}
type contextKey string

const adminKey contextKey = "admin"

func newApp(store *Store, uploadDir string) (*App, error) {
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return nil, err
	}
	assets, _ := fs.Sub(webFiles, "assets")
	a := &App{store: store, uploadDir: uploadDir, static: cacheControl("public, max-age=604800", http.StripPrefix("/assets/", http.FileServer(http.FS(assets)))), generalLimiter: newRateLimiter(400, 15*time.Minute), loginLimiter: newRateLimiter(5, 10*time.Minute), iconCache: map[string][]byte{}}
	r := http.NewServeMux()
	a.router = r
	r.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, object{"ok": true}) })
	r.HandleFunc("GET /api/site", a.site)
	r.HandleFunc("GET /api/videos/{id}/playback", a.videoPlayback)
	r.HandleFunc("POST /api/client-events", a.clientDiagnostics)
	r.HandleFunc("GET /robots.txt", a.robots)
	r.HandleFunc("GET /sitemap.xml", a.sitemap)
	r.HandleFunc("GET /site.webmanifest", a.siteManifest)
	r.HandleFunc("GET /favicon.ico", a.faviconRedirect)
	r.HandleFunc("GET /icons/site-32.png", a.siteIconImage)
	r.HandleFunc("GET /icons/site-180.png", a.siteIconImage)
	r.HandleFunc("GET /icons/site-192.png", a.siteIconImage)
	r.HandleFunc("GET /icons/site-512.png", a.siteIconImage)
	r.HandleFunc("POST /api/admin/login", a.login)
	r.Handle("POST /api/admin/logout", a.admin(http.HandlerFunc(a.logout)))
	r.Handle("GET /api/admin/bootstrap", a.admin(http.HandlerFunc(a.bootstrap)))
	r.Handle("GET /api/admin/preview", a.admin(http.HandlerFunc(a.preview)))
	r.Handle("PUT /api/admin/content", a.admin(http.HandlerFunc(a.saveContent)))
	r.Handle("POST /api/admin/publish", a.admin(http.HandlerFunc(a.publish)))
	r.Handle("POST /api/admin/revisions/{id}/restore", a.admin(http.HandlerFunc(a.restoreRevision)))
	r.Handle("DELETE /api/admin/revisions/{id}", a.admin(http.HandlerFunc(a.deleteRevision)))
	r.Handle("PUT /api/admin/settings/storage", a.admin(http.HandlerFunc(a.updateStorage)))
	r.Handle("POST /api/admin/media", a.admin(http.HandlerFunc(a.uploadMedia)))
	r.Handle("DELETE /api/admin/media/{id}", a.admin(http.HandlerFunc(a.removeMedia)))
	r.Handle("GET /api/admin/export", a.admin(http.HandlerFunc(a.export)))
	r.HandleFunc("GET /manage", a.manage)
	r.HandleFunc("GET /", a.home)
	r.HandleFunc("GET /index.html", a.home)
	r.HandleFunc("GET /video.html", a.video)
	r.Handle("GET /assets/", a.static)
	r.Handle("GET /uploads/", cacheControl("public, max-age=2592000, immutable", http.StripPrefix("/uploads/", http.FileServer(http.Dir(uploadDir)))))
	return a, nil
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	id := requestID()
	r = r.WithContext(context.WithValue(r.Context(), requestIDKey, id))
	w.Header().Set("X-Request-ID", id)
	metrics := &responseMetrics{ResponseWriter: w}
	w = metrics
	defer a.recordAccess(r, metrics, started)
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Origin-Agent-Cluster", "?1")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-DNS-Prefetch-Control", "off")
	w.Header().Set("X-Download-Options", "noopen")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("X-Permitted-Cross-Domain-Policies", "none")
	w.Header().Set("X-XSS-Protection", "0")
	limitedAPI := strings.HasPrefix(r.URL.Path, "/api/admin") || strings.HasPrefix(r.URL.Path, "/api/videos/") || r.URL.Path == "/api/client-events"
	if limitedAPI && !a.generalLimiter.allow(clientIP(r)) {
		writeJSON(w, 429, object{"error": "Too many requests, please try again later."})
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("panic request_id=%s: %v", requestIDFrom(r), rec)
			writeJSON(w, 500, object{"error": "服务器处理失败"})
		}
	}()
	a.router.ServeHTTP(w, r)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func cacheControl(value string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", value)
		next.ServeHTTP(w, r)
	})
}
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	d := json.NewDecoder(r.Body)
	if err := d.Decode(v); err != nil {
		writeJSON(w, 400, object{"error": "请求内容无效"})
		return err
	}
	return nil
}
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func hashToken(v string) string        { s := sha256.Sum256([]byte(v)); return hex.EncodeToString(s[:]) }
func adminName(r *http.Request) string { v, _ := r.Context().Value(adminKey).(string); return v }
func (a *App) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("portfolio_session")
		if err != nil {
			writeJSON(w, 401, object{"error": "需要管理员登录"})
			return
		}
		user, ok := a.store.Session(hashToken(cookie.Value))
		if !ok {
			writeJSON(w, 401, object{"error": "需要管理员登录"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminKey, user)))
	})
}
func (a *App) audit(r *http.Request, action, targetType, targetID, detail string) {
	user := adminName(r)
	if user == "" {
		var body object
		user = stringValue(body["username"])
		if user == "" {
			user = "system"
		}
	}
	if err := a.store.Log(AdminLog{Username: user, Action: action, TargetType: targetType, TargetID: targetID, Detail: detail, IPAddress: accessClientIP(r), RequestID: requestIDFrom(r)}); err != nil {
		log.Printf("Unable to write admin audit log: %v", err)
	}
}

func (a *App) site(w http.ResponseWriter, r *http.Request) {
	d, e := a.store.GetDocument("published")
	if e != nil {
		a.fail(w, e)
		return
	}
	d.Content, e = a.contentWithPublicMediaURLs(d.Content)
	if e != nil {
		a.fail(w, e)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, d.Content)
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !a.loginLimiter.allow(clientIP(r)) {
		writeJSON(w, 429, object{"error": "尝试次数过多，请十分钟后再试"})
		return
	}
	var p struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decodeJSON(w, r, &p) != nil {
		return
	}
	if p.Username == "" {
		p.Username = "admin"
	}
	hash, ok := a.store.UserHash(p.Username)
	if !ok || bcrypt.CompareHashAndPassword([]byte(hash), []byte(p.Password)) != nil {
		_ = a.store.Log(AdminLog{Username: p.Username, Action: "login_failed", TargetType: "session", Detail: "登录验证失败", IPAddress: accessClientIP(r), RequestID: requestIDFrom(r)})
		writeJSON(w, 401, object{"error": "用户名或密码错误"})
		return
	}
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	token := hex.EncodeToString(buf)
	expires := time.Now().Add(8 * time.Hour)
	if e := a.store.CreateSession(hashToken(token), p.Username, expires.UnixMilli()); e != nil {
		a.fail(w, e)
		return
	}
	a.loginLimiter.refund(clientIP(r))
	http.SetCookie(w, &http.Cookie{Name: "portfolio_session", Value: token, Path: "/", MaxAge: 28800, HttpOnly: true, Secure: os.Getenv("NODE_ENV") == "production", SameSite: http.SameSiteLaxMode})
	r = r.WithContext(context.WithValue(r.Context(), adminKey, p.Username))
	a.audit(r, "login_success", "session", "", "登录后台")
	writeJSON(w, 200, object{"ok": true, "username": p.Username})
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("portfolio_session"); e == nil {
		_ = a.store.DeleteSession(hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "portfolio_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	a.audit(r, "logout", "session", "", "退出后台")
	writeJSON(w, 200, object{"ok": true})
}
func (a *App) preview(w http.ResponseWriter, r *http.Request) {
	d, e := a.store.GetDocument("draft")
	if e != nil {
		a.fail(w, e)
		return
	}
	d.Content, e = a.contentWithPublicMediaURLs(d.Content)
	if e != nil {
		a.fail(w, e)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, d.Content)
}
func (a *App) bootstrap(w http.ResponseWriter, r *http.Request) {
	p, e := a.adminPayload()
	if e != nil {
		a.fail(w, e)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, p)
}
func (a *App) saveContent(w http.ResponseWriter, r *http.Request) {
	var content object
	if decodeJSON(w, r, &content) != nil {
		return
	}
	if !validContent(content) {
		writeJSON(w, 400, object{"error": "内容结构无效"})
		return
	}
	d, e := a.store.SaveDraft(content)
	if e != nil {
		a.fail(w, e)
		return
	}
	a.audit(r, "draft_saved", "content", "draft", "哈希 "+d.Hash)
	writeJSON(w, 200, d)
}
func (a *App) publish(w http.ResponseWriter, r *http.Request) {
	d, e := a.store.GetDocument("draft")
	if e != nil {
		a.fail(w, e)
		return
	}
	if issues := projectValidationIssues(d.Content); len(issues) > 0 {
		writeJSON(w, 400, object{"error": strings.Join(issues, "；")})
		return
	}
	if e = a.validateSiteIcon(d.Content); e != nil {
		writeJSON(w, 400, object{"error": e.Error()})
		return
	}
	result, e := a.store.Publish(adminName(r))
	if e != nil {
		a.fail(w, e)
		return
	}
	changed, _ := result["changed"].(bool)
	if changed {
		a.iconMu.Lock()
		a.iconCache = map[string][]byte{}
		a.iconMu.Unlock()
	}
	id := fmt.Sprint(result["id"])
	if !changed {
		id = ""
		a.audit(r, "publish_skipped", "revision", "", fmt.Sprintf("内容哈希未变化，未创建重复版本（%s）", result["hash"]))
	} else {
		a.audit(r, "site_published", "revision", id, fmt.Sprintf("发布版本 #%s，哈希 %s", id, result["hash"]))
	}
	writeJSON(w, 200, result)
}
func (a *App) restoreRevision(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	d, ok, e := a.store.RestoreRevision(id)
	if e != nil {
		a.fail(w, e)
		return
	}
	if !ok {
		writeJSON(w, 404, object{"error": "版本不存在"})
		return
	}
	a.audit(r, "revision_restored", "revision", r.PathValue("id"), "恢复为草稿，哈希 "+d.Hash)
	writeJSON(w, 200, d)
}
func (a *App) deleteRevision(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	d, ok, e := a.store.DeleteRevision(id)
	if e != nil {
		a.fail(w, e)
		return
	}
	if !ok {
		writeJSON(w, 404, object{"error": "版本不存在"})
		return
	}
	a.audit(r, "revision_deleted", "revision", fmt.Sprint(d["id"]), fmt.Sprintf("删除历史版本，哈希 %s", d["hash"]))
	w.WriteHeader(204)
}
func (a *App) updateStorage(w http.ResponseWriter, r *http.Request) {
	var p object
	if decodeJSON(w, r, &p) != nil {
		return
	}
	provider := stringValue(p["provider"])
	if provider != "local" && provider != "cos" {
		writeJSON(w, 400, object{"error": "无效的存储类型"})
		return
	}
	cfg := a.cosConfig()
	if provider == "cos" && !cfg.Ready {
		writeJSON(w, 400, object{"error": "腾讯云 COS 配置不完整"})
		return
	}
	previous := fallbackString(a.store.Setting("storage_provider"), "local")
	if e := a.store.SetSetting("storage_provider", provider); e != nil {
		a.fail(w, e)
		return
	}
	a.audit(r, "storage_updated", "setting", "storage_provider", previous+" → "+provider)
	status := a.storageStatus()
	status["vod"] = a.vodStatus()
	writeJSON(w, 200, status)
}
func (a *App) export(w http.ResponseWriter, r *http.Request) {
	draft, e := a.store.GetDocument("draft")
	if e != nil {
		a.fail(w, e)
		return
	}
	published, e := a.store.GetDocument("published")
	if e != nil {
		a.fail(w, e)
		return
	}
	revs, e := a.store.ListRevisions()
	if e != nil {
		a.fail(w, e)
		return
	}
	logs, e := a.store.ListLogs(500)
	if e != nil {
		a.fail(w, e)
		return
	}
	media, e := a.store.ListMedia()
	if e != nil {
		a.fail(w, e)
		return
	}
	a.audit(r, "backup_exported", "backup", "", "导出完整 JSON 备份")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="portfolio-cms-backup.json"`)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	publicMedia := make([]object, 0, len(media))
	cfg := a.cosConfig()
	for _, m := range media {
		publicMedia = append(publicMedia, publicMediaObject(m, cfg))
	}
	_ = enc.Encode(object{"exportedAt": time.Now().UTC().Format(time.RFC3339Nano), "draft": draft, "published": published, "revisions": revs, "logs": logs, "media": publicMedia})
}

func (a *App) adminPayload() (object, error) {
	draft, e := a.store.GetDocument("draft")
	if e != nil {
		return nil, e
	}
	published, e := a.store.GetDocument("published")
	if e != nil {
		return nil, e
	}
	media, e := a.store.ListMedia()
	if e != nil {
		return nil, e
	}
	revs, e := a.store.ListRevisions()
	if e != nil {
		return nil, e
	}
	logs, e := a.store.ListLogs(200)
	if e != nil {
		return nil, e
	}
	items := make([]object, 0, len(media))
	cfg := a.cosConfig()
	for _, m := range media {
		item := publicMediaObject(m, cfg)
		display, err := signedPublicMedia(m, cfg)
		if err != nil {
			return nil, err
		}
		item["originalDisplayUrl"] = display.OriginalURL
		item["optimizedDisplayUrl"] = display.OptimizedURL
		item["originalName"] = normalizeUploadFilename(m.OriginalName)
		item["references"] = a.referencedBy(m)
		items = append(items, item)
	}
	settings := a.storageStatus()
	settings["vod"] = a.vodStatus()
	return object{"content": draft.Content, "draftUpdatedAt": draft.UpdatedAt, "publishedUpdatedAt": published.UpdatedAt, "versionState": object{"draftHash": draft.Hash, "publishedHash": published.Hash, "hasUnpublishedChanges": draft.Hash != published.Hash}, "frontendUrl": os.Getenv("FRONTEND_URL"), "revisions": revs, "media": items, "settings": settings, "logs": logs, "stats": object{"experiences": len(asArray(draft.Content["experiences"])), "caseStudies": len(asArray(draft.Content["caseStudies"])), "projects": len(asArray(draft.Content["projects"])), "media": len(media)}}, nil
}
func (a *App) referencedBy(m Media) []string {
	hits := make([]string, 0)
	public := effectivePublicMedia(m, a.cosConfig())
	candidates := []string{m.OptimizedURL, m.OriginalURL, public.OptimizedURL, public.OriginalURL}
	if m.OptimizedKey != "" {
		candidates = append(candidates, "/"+m.OptimizedKey)
	}
	if m.OriginalKey != "" {
		candidates = append(candidates, "/"+m.OriginalKey)
	}
	for _, name := range []string{"draft", "published"} {
		d, e := a.store.GetDocument(name)
		if e != nil {
			continue
		}
		b, _ := json.Marshal(d.Content)
		referenced := siteIconMediaID(d.Content) == m.ID
		for _, candidate := range candidates {
			if candidate != "" && strings.Contains(string(b), candidate) {
				referenced = true
				break
			}
		}
		if referenced {
			if name == "draft" {
				hits = append(hits, "草稿")
			} else {
				hits = append(hits, "已发布内容")
			}
		}
	}
	return hits
}

func effectivePublicMedia(m Media, cfg cosConfiguration) Media {
	if m.Storage == "cos" && cfg.PublicBase != "" {
		if m.OriginalKey != "" {
			m.OriginalURL = publicCOSURL(m.OriginalKey, cfg)
		}
		if m.OptimizedKey != "" {
			m.OptimizedURL = publicCOSURL(m.OptimizedKey, cfg)
		}
	}
	return m
}

func signedPublicMedia(m Media, cfg cosConfiguration) (Media, error) {
	m = effectivePublicMedia(m, cfg)
	if m.Storage != "cos" || !cfg.Ready {
		return m, nil
	}
	var err error
	if m.OriginalKey != "" {
		m.OriginalURL, err = signedCOSURL(m.OriginalKey, cfg)
		if err != nil {
			return Media{}, err
		}
	}
	if m.OptimizedKey != "" {
		m.OptimizedURL, err = signedCOSURL(m.OptimizedKey, cfg)
		if err != nil {
			return Media{}, err
		}
	}
	return m, nil
}

func rewriteMediaURLs(content object, media []Media, cfg cosConfiguration) error {
	replacements := make(map[string]string)
	keyReplacements := make(map[string]string)
	for _, stored := range media {
		stable := effectivePublicMedia(stored, cfg)
		public, err := signedPublicMedia(stored, cfg)
		if err != nil {
			return err
		}
		if stored.OriginalURL != "" && stored.OriginalURL != public.OriginalURL {
			replacements[stored.OriginalURL] = public.OriginalURL
		}
		if stored.OptimizedURL != "" && stored.OptimizedURL != public.OptimizedURL {
			replacements[stored.OptimizedURL] = public.OptimizedURL
		}
		if stable.OriginalURL != "" && stable.OriginalURL != public.OriginalURL {
			replacements[stable.OriginalURL] = public.OriginalURL
		}
		if stable.OptimizedURL != "" && stable.OptimizedURL != public.OptimizedURL {
			replacements[stable.OptimizedURL] = public.OptimizedURL
		}
		if stored.OriginalKey != "" {
			keyReplacements["/"+stored.OriginalKey] = public.OriginalURL
		}
		if stored.OptimizedKey != "" {
			keyReplacements["/"+stored.OptimizedKey] = public.OptimizedURL
		}
	}
	var rewrite func(any) any
	rewrite = func(value any) any {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				typed[key] = rewrite(child)
			}
		case []any:
			for index, child := range typed {
				typed[index] = rewrite(child)
			}
		case string:
			if replacement, ok := replacements[typed]; ok {
				return replacement
			}
			if parsed, err := url.Parse(typed); err == nil {
				if replacement, ok := keyReplacements[parsed.Path]; ok {
					return replacement
				}
			}
		}
		return value
	}
	rewrite(content)
	return nil
}

func (a *App) contentWithPublicMediaURLs(content object) (object, error) {
	media, err := a.store.ListMedia()
	if err != nil {
		return nil, err
	}
	if err = rewriteMediaURLs(content, media, a.cosConfig()); err != nil {
		return nil, err
	}
	return content, nil
}

func publicMediaObject(m Media, cfg cosConfiguration) object {
	m = effectivePublicMedia(m, cfg)
	return object{"id": m.ID, "originalName": m.OriginalName, "storage": m.Storage, "originalUrl": m.OriginalURL, "optimizedUrl": m.OptimizedURL, "mimeType": m.MimeType, "width": m.Width, "height": m.Height, "size": m.Size, "createdAt": m.CreatedAt}
}
func (a *App) fail(w http.ResponseWriter, e error) {
	log.Printf("request failed request_id=%s: %v", w.Header().Get("X-Request-ID"), e)
	writeJSON(w, 500, object{"error": "服务器处理失败"})
}

func (a *App) manage(w http.ResponseWriter, r *http.Request) {
	serveEmbedded(w, r, "server/admin.html", 0)
}
func (a *App) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	a.renderPage(w, r, "index.html", false)
}
func (a *App) video(w http.ResponseWriter, r *http.Request) { a.renderPage(w, r, "video.html", true) }
func serveEmbedded(w http.ResponseWriter, r *http.Request, name string, maxAge int) {
	b, e := webFiles.ReadFile(name)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	if maxAge > 0 {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
}
func (a *App) robots(w http.ResponseWriter, r *http.Request) {
	d, e := a.store.GetDocument("published")
	if e != nil {
		a.fail(w, e)
		return
	}
	seo := asObject(d.Content["seo"])
	rules := "User-agent: *\nAllow: /\nDisallow: /manage\nDisallow: /api/admin"
	if !boolValue(seo["allowIndexing"], true) {
		rules = "User-agent: *\nDisallow: /"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "%s\nSitemap: %s/sitemap.xml\n", rules, publicBaseURL(r, seo))
}
func (a *App) sitemap(w http.ResponseWriter, r *http.Request) {
	d, e := a.store.GetDocument("published")
	if e != nil {
		a.fail(w, e)
		return
	}
	base := publicBaseURL(r, asObject(d.Content["seo"]))
	urls := []string{base + "/"}
	for _, raw := range asArray(d.Content["projects"]) {
		item := asObject(raw)
		if boolValue(item["published"], true) && stringValue(item["type"]) == "video" && stringValue(item["vodFileId"]) != "" {
			urls = append(urls, base+"/video.html?id="+encodeURIComponent(stringValue(item["id"])))
		}
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, u := range urls {
		b.WriteString("<url><loc>" + html.EscapeString(u) + "</loc></url>")
	}
	b.WriteString("</urlset>")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	io.WriteString(w, b.String())
}

var titleRE = regexp.MustCompile(`(?is)<title>.*?</title>`)
var descriptionRE = regexp.MustCompile(`(?i)<meta\s+name="description"[^>]*>`)
var namedMetaRE = regexp.MustCompile(`(?i)<meta\s+name="(?:keywords|author|robots)"[^>]*>`)
var ogRE = regexp.MustCompile(`(?i)<meta\s+property="og:[^"]+"[^>]*>`)
var twitterRE = regexp.MustCompile(`(?i)<meta\s+name="twitter:[^"]+"[^>]*>`)
var canonicalRE = regexp.MustCompile(`(?i)<link\s+rel="canonical"[^>]*>`)
var appIconLinkRE = regexp.MustCompile(`(?i)<link\s+rel="(?:icon|apple-touch-icon|manifest)"[^>]*>`)
var schemaRE = regexp.MustCompile(`(?is)<script\s+type="application/ld\+json"[^>]*>.*?</script>`)

func (a *App) renderPage(w http.ResponseWriter, r *http.Request, name string, isVideo bool) {
	d, e := a.store.GetDocument("published")
	if e != nil {
		a.fail(w, e)
		return
	}
	content, e := a.contentWithPublicMediaURLs(d.Content)
	if e != nil {
		a.fail(w, e)
		return
	}
	seo := asObject(content["seo"])
	base := publicBaseURL(r, seo)
	var project object
	if isVideo {
		for _, raw := range asArray(content["projects"]) {
			item := asObject(raw)
			if stringValue(item["id"]) == r.URL.Query().Get("id") && stringValue(item["type"]) == "video" && boolValue(item["published"], true) {
				project = item
				break
			}
		}
	}
	profile := asObject(content["profile"])
	title := fallbackString(stringValue(seo["title"]), fallbackString(stringValue(profile["englishName"]), "Your Name")+" — 作品档案")
	description := stringValue(seo["description"])
	canonical := base + "/"
	ogType := "website"
	imageURL := stringValue(seo["socialImage"])
	if project != nil {
		title = stringValue(project["title"]) + "｜" + fallbackString(stringValue(profile["siteName"]), "Your Portfolio")
		description = fallbackString(stringValue(project["description"]), description)
		canonical = base + "/video.html?id=" + encodeURIComponent(stringValue(project["id"]))
		ogType = "video.other"
		imageURL = fallbackString(stringValue(project["coverUrl"]), imageURL)
	}
	socialTitle := fallbackString(stringValue(seo["socialTitle"]), title)
	socialDescription := fallbackString(stringValue(seo["socialDescription"]), description)
	if project != nil {
		socialTitle = title
		socialDescription = description
	}
	imageURL = absoluteURL(imageURL, base)
	robots := "index,follow"
	if !boolValue(seo["allowIndexing"], true) {
		robots = "noindex,nofollow"
	}
	esc := html.EscapeString
	tags := []string{"<title>" + esc(title) + "</title>", `<meta name="description" content="` + esc(description) + `">`}
	iconVersion := d.Hash
	if len(iconVersion) > 12 {
		iconVersion = iconVersion[:12]
	}
	if siteIconMediaID(content) > 0 {
		tags = append(tags,
			`<link rel="icon" type="image/png" sizes="32x32" href="/icons/site-32.png?v=`+iconVersion+`">`,
			`<link rel="apple-touch-icon" sizes="180x180" href="/icons/site-180.png?v=`+iconVersion+`">`,
		)
	} else {
		tags = append(tags, `<link rel="icon" href="/assets/images/logo.ico">`)
	}
	tags = append(tags, `<link rel="manifest" href="/site.webmanifest?v=`+iconVersion+`">`)
	if v := stringValue(seo["keywords"]); v != "" {
		tags = append(tags, `<meta name="keywords" content="`+esc(v)+`">`)
	}
	if v := stringValue(seo["author"]); v != "" {
		tags = append(tags, `<meta name="author" content="`+esc(v)+`">`)
	}
	tags = append(tags, `<meta name="robots" content="`+robots+`">`, `<link rel="canonical" href="`+esc(canonical)+`">`, `<meta property="og:locale" content="zh_CN">`, `<meta property="og:type" content="`+ogType+`">`, `<meta property="og:site_name" content="`+esc(fallbackString(stringValue(profile["siteName"]), "Your Portfolio"))+`">`, `<meta property="og:title" content="`+esc(socialTitle)+`">`, `<meta property="og:description" content="`+esc(socialDescription)+`">`, `<meta property="og:url" content="`+esc(canonical)+`">`)
	if imageURL != "" {
		tags = append(tags, `<meta property="og:image" content="`+esc(imageURL)+`">`)
	}
	card := "summary"
	if imageURL != "" {
		card = "summary_large_image"
	}
	tags = append(tags, `<meta name="twitter:card" content="`+card+`">`, `<meta name="twitter:title" content="`+esc(socialTitle)+`">`, `<meta name="twitter:description" content="`+esc(socialDescription)+`">`)
	if imageURL != "" {
		tags = append(tags, `<meta name="twitter:image" content="`+esc(imageURL)+`">`)
	}
	if boolValue(seo["enableStructuredData"], true) && project == nil {
		same := []any{}
		for _, raw := range asArray(content["socials"]) {
			item := asObject(raw)
			if boolValue(item["visible"], true) && stringValue(item["url"]) != "" {
				same = append(same, item["url"])
			}
		}
		person := object{"@context": "https://schema.org", "@type": "Person", "name": fallbackString(stringValue(profile["name"]), stringValue(seo["author"])), "alternateName": stringValue(profile["englishName"]), "url": canonical, "image": absoluteURL(stringValue(profile["avatarUrl"]), base), "jobTitle": stringValue(profile["title"]), "email": stringValue(profile["email"]), "address": stringValue(profile["location"]), "sameAs": same}
		b, _ := json.Marshal(person)
		tags = append(tags, `<script type="application/ld+json" data-seo-schema>`+strings.ReplaceAll(string(b), "<", `\u003c`)+`</script>`)
	}
	raw, e := webFiles.ReadFile(name)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	page := string(raw)
	for _, re := range []*regexp.Regexp{titleRE, descriptionRE, namedMetaRE, ogRE, twitterRE, canonicalRE, schemaRE, appIconLinkRE} {
		page = re.ReplaceAllString(page, "")
	}
	page = strings.Replace(page, "</head>", strings.Join(tags, "")+"\n</head>", 1)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, page)
}
func publicBaseURL(r *http.Request, seo object) string {
	if v := strings.TrimSuffix(strings.TrimSpace(stringValue(seo["siteUrl"])), "/"); strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return v
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
func absoluteURL(value, base string) string {
	if value == "" {
		return ""
	}
	u, e := url.Parse(value)
	if e != nil {
		return ""
	}
	b, e := url.Parse(base + "/")
	if e != nil {
		return ""
	}
	return b.ResolveReference(u).String()
}

func encodeURIComponent(v string) string { return strings.ReplaceAll(url.QueryEscape(v), "+", "%20") }

type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, hits: map[string][]time.Time{}}
}
func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cut := now.Add(-l.window)
	old := l.hits[key]
	i := 0
	for i < len(old) && old[i].Before(cut) {
		i++
	}
	old = old[i:]
	if len(old) >= l.limit {
		l.hits[key] = old
		return false
	}
	l.hits[key] = append(old, now)
	return true
}

func (l *rateLimiter) refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	old := l.hits[key]
	if len(old) > 0 {
		l.hits[key] = old[:len(old)-1]
	}
}
