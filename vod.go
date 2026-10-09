package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type vodConfiguration struct {
	Ready       bool
	Missing     []string
	AppID       int64
	AppIDText   string
	PlaybackKey string
	LicenseURL  string
	TTL         time.Duration
}

func (a *App) vodConfig() vodConfiguration {
	required := []string{"TENCENT_VOD_APP_ID", "TENCENT_VOD_PLAYBACK_KEY", "TENCENT_VOD_LICENSE_URL"}
	missing := make([]string, 0)
	for _, key := range required {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			missing = append(missing, key)
		}
	}
	appIDText := strings.TrimSpace(os.Getenv("TENCENT_VOD_APP_ID"))
	appID, err := strconv.ParseInt(appIDText, 10, 64)
	if appIDText != "" && (err != nil || appID <= 0) {
		missing = append(missing, "TENCENT_VOD_APP_ID (must be a positive integer)")
	}
	ttl := 10 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("TENCENT_VOD_SIGNATURE_TTL")); raw != "" {
		parsed, parseErr := time.ParseDuration(raw)
		if parseErr != nil || parsed < time.Minute || parsed > 24*time.Hour {
			missing = append(missing, "TENCENT_VOD_SIGNATURE_TTL (must be between 1m and 24h)")
		} else {
			ttl = parsed
		}
	}
	return vodConfiguration{
		Ready:       len(missing) == 0,
		Missing:     missing,
		AppID:       appID,
		AppIDText:   appIDText,
		PlaybackKey: os.Getenv("TENCENT_VOD_PLAYBACK_KEY"),
		LicenseURL:  strings.TrimSpace(os.Getenv("TENCENT_VOD_LICENSE_URL")),
		TTL:         ttl,
	}
}

func (a *App) vodStatus() object {
	cfg := a.vodConfig()
	return object{"ready": cfg.Ready, "missing": cfg.Missing, "appId": cfg.AppIDText, "licenseConfigured": cfg.LicenseURL != "", "signatureTTL": cfg.TTL.String()}
}

func createVODPlayerSignature(appID int64, fileID, key string, now time.Time, ttl time.Duration) (string, int64, error) {
	if appID <= 0 || strings.TrimSpace(fileID) == "" || key == "" {
		return "", 0, fmt.Errorf("invalid VOD signing parameters")
	}
	expiresAt := now.Add(ttl).Unix()
	header := object{"alg": "HS256", "typ": "JWT"}
	payload := object{
		"appId":            appID,
		"fileId":           fileID,
		"contentInfo":      object{"audioVideoType": "Original"},
		"currentTimeStamp": now.Unix(),
		"expireTimeStamp":  expiresAt,
	}
	encode := func(value any) (string, error) {
		data, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(data), nil
	}
	encodedHeader, err := encode(header)
	if err != nil {
		return "", 0, err
	}
	encodedPayload, err := encode(payload)
	if err != nil {
		return "", 0, err
	}
	unsigned := encodedHeader + "." + encodedPayload
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(unsigned))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return unsigned + "." + signature, expiresAt, nil
}

func (a *App) videoPlayback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	document, err := a.store.GetDocument("published")
	if err != nil {
		a.fail(w, err)
		return
	}
	var project object
	for _, raw := range asArray(document.Content["projects"]) {
		item := asObject(raw)
		if stringValue(item["id"]) == r.PathValue("id") && stringValue(item["type"]) == "video" && boolValue(item["published"], true) && stringValue(item["vodFileId"]) != "" {
			project = item
			break
		}
	}
	if project == nil {
		a.logDiagnostic(r, "vod_playback_rejected", "video", r.PathValue("id"), "未找到已发布且配置了 VOD FileID 的视频作品")
		writeJSON(w, http.StatusNotFound, object{"error": "视频不存在或尚未发布"})
		return
	}
	cfg := a.vodConfig()
	if !cfg.Ready {
		a.logDiagnostic(r, "vod_config_error", "video", r.PathValue("id"), "缺少或无效的配置："+strings.Join(cfg.Missing, ", "))
		writeJSON(w, http.StatusServiceUnavailable, object{"error": "视频播放服务暂未配置完成"})
		return
	}
	psign, expiresAt, err := createVODPlayerSignature(cfg.AppID, stringValue(project["vodFileId"]), cfg.PlaybackKey, time.Now(), cfg.TTL)
	if err != nil {
		a.logDiagnostic(r, "vod_signing_error", "video", r.PathValue("id"), err.Error())
		a.fail(w, err)
		return
	}
	a.logDiagnostic(r, "vod_playback_issued", "video", r.PathValue("id"), fmt.Sprintf("播放配置已下发 · AppID=%s · FileID=%s · 签名过期=%d · License=%s", cfg.AppIDText, diagnosticText(stringValue(project["vodFileId"]), 120), expiresAt, diagnosticURL(cfg.LicenseURL)))
	writeJSON(w, http.StatusOK, object{"appId": cfg.AppIDText, "fileId": stringValue(project["vodFileId"]), "psign": psign, "licenseUrl": cfg.LicenseURL, "expiresAt": expiresAt})
}
