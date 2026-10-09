package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type clientDiagnostic struct {
	Type        string `json:"type"`
	ProjectID   string `json:"projectId"`
	Stage       string `json:"stage"`
	Code        string `json:"code"`
	Message     string `json:"message"`
	ResourceURL string `json:"resourceUrl"`
	PageURL     string `json:"pageUrl"`
	RequestID   string `json:"requestId"`
}

var diagnosticSecretRE = regexp.MustCompile(`(?i)(psign|q-signature|token|authorization)\s*[=:]\s*[^\s·&]+`)
var diagnosticJWTRE = regexp.MustCompile(`[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)

func diagnosticText(value string, limit int) string {
	value = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(value))
	value = diagnosticSecretRE.ReplaceAllString(value, "$1=[redacted]")
	value = diagnosticJWTRE.ReplaceAllString(value, "[redacted-jwt]")
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit]) + "…"
	}
	return value
}

func diagnosticURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return diagnosticText(value, 300)
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return diagnosticText(parsed.String(), 300)
}

func (a *App) logDiagnostic(r *http.Request, action, targetType, targetID, detail string) {
	entry := AdminLog{Username: "frontend", Action: action, TargetType: targetType, TargetID: diagnosticText(targetID, 120), Detail: diagnosticText(detail, 1200), IPAddress: accessClientIP(r), RequestID: requestIDFrom(r)}
	if err := a.store.Log(entry); err != nil {
		fmt.Printf("unable to persist diagnostic log: %v\n", err)
	}
}

func (a *App) clientDiagnostics(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var event clientDiagnostic
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&event); err != nil {
		writeJSON(w, http.StatusBadRequest, object{"error": "诊断信息无效"})
		return
	}
	action, targetType := "", ""
	switch event.Type {
	case "vod_error":
		action, targetType = "vod_client_error", "video"
	case "media_error":
		action, targetType = "media_client_error", "media"
	default:
		writeJSON(w, http.StatusBadRequest, object{"error": "不支持的诊断类型"})
		return
	}
	detail := fmt.Sprintf("阶段=%s · 错误码=%s · 信息=%s · 关联请求=%s · 资源=%s · 页面=%s · 浏览器=%s",
		diagnosticText(event.Stage, 80), diagnosticText(event.Code, 80), diagnosticText(event.Message, 400), diagnosticText(event.RequestID, 80), diagnosticURL(event.ResourceURL), diagnosticURL(event.PageURL), diagnosticText(r.UserAgent(), 240))
	a.logDiagnostic(r, action, targetType, event.ProjectID, detail)
	w.WriteHeader(http.StatusNoContent)
}
