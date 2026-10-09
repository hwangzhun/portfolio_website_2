package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const requestIDKey contextKey = "request-id"

type responseMetrics struct {
	http.ResponseWriter
	status int
	bytes  int64
}

// Unwrap lets http.ResponseController retain optional capabilities such as
// flushing and hijacking when a handler needs them.
func (w *responseMetrics) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseMetrics) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseMetrics) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func requestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return hex.EncodeToString(b)
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func requestIDFrom(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey).(string)
	return id
}

func envBool(name string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	return err == nil && v
}

func accessClientIP(r *http.Request) string {
	if envBool("TRUST_PROXY", false) {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
			return diagnosticText(forwarded, 80)
		}
		if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
			return diagnosticText(realIP, 80)
		}
	}
	return clientIP(r)
}

func shouldPersistAccess(r *http.Request, status int) bool {
	if r.URL.Path == "/api/health" && !envBool("ACCESS_LOG_HEALTH", false) {
		return status >= 400
	}
	static := strings.HasPrefix(r.URL.Path, "/assets/") || strings.HasPrefix(r.URL.Path, "/uploads/")
	if static && !envBool("ACCESS_LOG_STATIC", false) {
		return status >= 400
	}
	return true
}

func accessPath(r *http.Request) string {
	path := r.URL.EscapedPath()
	// The video project id is useful for diagnosis. Other query values are omitted
	// so access logs cannot accidentally retain tokens or signed URLs.
	if r.URL.Path == "/video.html" {
		if id := diagnosticText(r.URL.Query().Get("id"), 120); id != "" {
			path += "?id=" + id
		}
	}
	return diagnosticText(path, 500)
}

func (a *App) recordAccess(r *http.Request, metrics *responseMetrics, started time.Time) {
	status := metrics.status
	if status == 0 {
		status = http.StatusOK
	}
	duration := time.Since(started)
	id := requestIDFrom(r)
	path := accessPath(r)
	log.Printf("access request_id=%s method=%s path=%q status=%d bytes=%d duration_ms=%d ip=%q", id, r.Method, path, status, metrics.bytes, duration.Milliseconds(), accessClientIP(r))
	if !shouldPersistAccess(r, status) {
		return
	}
	detail := fmt.Sprintf("%s %s · %d %s · %d B · %d ms", r.Method, path, status, http.StatusText(status), metrics.bytes, duration.Milliseconds())
	entry := AdminLog{
		Username: "system", Action: "http_request", TargetType: "request", TargetID: id,
		Detail: detail, IPAddress: accessClientIP(r), RequestID: id, Method: r.Method,
		Path: path, StatusCode: status, DurationMS: duration.Milliseconds(), ResponseBytes: metrics.bytes,
		UserAgent: diagnosticText(r.UserAgent(), 300), Referer: diagnosticURL(r.Referer()),
	}
	if err := a.store.Log(entry); err != nil {
		log.Printf("unable to persist access log request_id=%s: %v", id, err)
		return
	}
	if a.accessLogCount.Add(1)%250 == 0 {
		if err := a.store.PruneLogs(10000); err != nil {
			log.Printf("unable to prune access logs: %v", err)
		}
	}
}

func (a *App) recordStartup(port string) {
	vod := a.vodConfig()
	storage := fallbackString(a.store.Setting("storage_provider"), "local")
	detail := fmt.Sprintf("监听端口=%s · 存储=%s · VOD ready=%t", diagnosticText(port, 20), storage, vod.Ready)
	if !vod.Ready {
		detail += " · VOD 缺少/无效=" + strings.Join(vod.Missing, ", ")
	} else {
		detail += fmt.Sprintf(" · VOD AppID=%s · 签名有效期=%s", vod.AppIDText, vod.TTL)
	}
	log.Printf("system start: %s", detail)
	if err := a.store.Log(AdminLog{Username: "system", Action: "system_start", TargetType: "runtime", Detail: detail}); err != nil {
		log.Printf("unable to persist startup log: %v", err)
	}
}
