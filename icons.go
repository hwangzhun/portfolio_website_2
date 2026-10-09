package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"
)

var siteIconSizes = map[int]bool{32: true, 180: true, 192: true, 512: true}

func validSiteIconMedia(m Media) bool {
	validType := m.MimeType == "image/jpeg" || m.MimeType == "image/png" || m.MimeType == "image/webp"
	return validType && m.Width == m.Height && m.Width >= 512
}

func (a *App) validateSiteIcon(content object) error {
	id := siteIconMediaID(content)
	if id == 0 {
		return nil
	}
	m, ok, err := a.store.GetMedia(id)
	if err != nil {
		return fmt.Errorf("无法验证网站图标")
	}
	if !ok {
		return fmt.Errorf("网站图标已不在媒体库中，请重新选择")
	}
	if !validSiteIconMedia(m) {
		return fmt.Errorf("网站图标必须是不小于 512×512 的正方形 JPG、PNG 或 WebP 图片")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	raw, err := a.readIconSource(ctx, m)
	if err != nil {
		return fmt.Errorf("无法读取网站图标原图，请重新上传或检查存储配置")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "jpeg" && format != "png" && format != "webp") || config.Width != config.Height || config.Width < 512 {
		return fmt.Errorf("网站图标原图无效，请使用不小于 512×512 的正方形 JPG、PNG 或 WebP 图片")
	}
	return nil
}

func (a *App) publishedIcon() (Document, Media, bool, error) {
	d, err := a.store.GetDocument("published")
	if err != nil {
		return Document{}, Media{}, false, err
	}
	id := siteIconMediaID(d.Content)
	if id == 0 {
		return d, Media{}, false, nil
	}
	m, ok, err := a.store.GetMedia(id)
	if err != nil || !ok || !validSiteIconMedia(m) {
		return d, Media{}, false, err
	}
	return d, m, true, nil
}

func (a *App) readIconSource(ctx context.Context, m Media) ([]byte, error) {
	if m.Storage == "local" {
		path, err := safePath(a.uploadDir, m.OriginalKey)
		if err != nil {
			return nil, err
		}
		return osReadFile(path)
	}
	cfg := a.cosConfig()
	if !cfg.Ready {
		return nil, fmt.Errorf("COS 配置不完整")
	}
	client, err := cosClient(cfg)
	if err != nil {
		return nil, err
	}
	response, err := client.Object.Get(ctx, m.OriginalKey, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return io.ReadAll(io.LimitReader(response.Body, (12<<20)+1))
}

var osReadFile = func(name string) ([]byte, error) { return os.ReadFile(name) }

func resizeIcon(raw []byte, mime string, size int) ([]byte, error) {
	source, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if mime == "image/jpeg" {
		source = orientImage(source, jpegOrientation(raw))
	}
	bounds := source.Bounds()
	if bounds.Dx() != bounds.Dy() || bounds.Dx() < size {
		return nil, fmt.Errorf("图标尺寸不合格")
	}
	destination := image.NewNRGBA(image.Rect(0, 0, size, size))
	xdraw.CatmullRom.Scale(destination, destination.Bounds(), source, bounds, draw.Src, nil)
	var output bytes.Buffer
	if err = png.Encode(&output, destination); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (a *App) generatedIcon(r *http.Request, m Media, size int) ([]byte, error) {
	key := fmt.Sprintf("%d:%d", m.ID, size)
	a.iconMu.Lock()
	cached := a.iconCache[key]
	a.iconMu.Unlock()
	if cached != nil {
		return cached, nil
	}
	raw, err := a.readIconSource(r.Context(), m)
	if err != nil {
		return nil, err
	}
	generated, err := resizeIcon(raw, m.MimeType, size)
	if err != nil {
		return nil, err
	}
	a.iconMu.Lock()
	a.iconCache[key] = generated
	a.iconMu.Unlock()
	return generated, nil
}

func (a *App) siteIconImage(w http.ResponseWriter, r *http.Request) {
	rawSize := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/icons/site-"), ".png")
	size, err := strconv.Atoi(rawSize)
	if err != nil || !siteIconSizes[size] {
		http.NotFound(w, r)
		return
	}
	d, media, ok, err := a.publishedIcon()
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := a.generatedIcon(r, media, size)
	if err != nil {
		a.fail(w, err)
		return
	}
	version := d.Hash
	if len(version) > 12 {
		version = version[:12]
	}
	if r.URL.Query().Get("v") == version {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

func (a *App) faviconRedirect(w http.ResponseWriter, r *http.Request) {
	d, _, ok, err := a.publishedIcon()
	if err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	if !ok {
		http.Redirect(w, r, "/assets/images/logo.ico", http.StatusTemporaryRedirect)
		return
	}
	version := d.Hash
	if len(version) > 12 {
		version = version[:12]
	}
	http.Redirect(w, r, "/icons/site-32.png?v="+version, http.StatusTemporaryRedirect)
}

func (a *App) siteManifest(w http.ResponseWriter, r *http.Request) {
	d, _, hasIcon, err := a.publishedIcon()
	if err != nil {
		a.fail(w, err)
		return
	}
	profile := asObject(d.Content["profile"])
	seo := asObject(d.Content["seo"])
	name := fallbackString(stringValue(profile["siteName"]), fallbackString(stringValue(profile["englishName"]), "Portfolio"))
	version := d.Hash
	if len(version) > 12 {
		version = version[:12]
	}
	icons := []any{object{"src": "/assets/images/logo.ico", "sizes": "256x256", "type": "image/x-icon", "purpose": "any"}}
	if hasIcon {
		icons = []any{
			object{"src": "/icons/site-192.png?v=" + version, "sizes": "192x192", "type": "image/png", "purpose": "any"},
			object{"src": "/icons/site-512.png?v=" + version, "sizes": "512x512", "type": "image/png", "purpose": "any"},
		}
	}
	manifest := object{"name": name, "short_name": name, "description": stringValue(seo["description"]), "id": "/", "start_url": "/", "scope": "/", "display": "browser", "icons": icons}
	w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(manifest)
}
