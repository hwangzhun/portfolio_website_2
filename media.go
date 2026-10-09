package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/KarpelesLab/gowebp"
	cos "github.com/tencentyun/cos-go-sdk-v5"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

type cosConfiguration struct {
	Ready                      bool
	Missing                    []string
	Bucket, Region, PublicBase string
	SignedURLTTL               time.Duration
}

var cosEndpointOverride string

func (a *App) cosConfig() cosConfiguration {
	required := []string{"TENCENT_COS_SECRET_ID", "TENCENT_COS_SECRET_KEY", "TENCENT_COS_BUCKET", "TENCENT_COS_REGION"}
	missing := make([]string, 0)
	for _, k := range required {
		if os.Getenv(k) == "" {
			missing = append(missing, k)
		}
	}
	publicVariable := "TENCENT_COS_CUSTOM_DOMAIN"
	publicRaw := strings.TrimSpace(os.Getenv(publicVariable))
	if publicRaw == "" {
		publicVariable = "TENCENT_COS_CDN_URL"
		publicRaw = strings.TrimSpace(os.Getenv(publicVariable))
	}
	publicBase := normalizeCOSPublicBase(publicRaw)
	if publicRaw != "" && publicBase == "" {
		missing = append(missing, publicVariable+" (invalid URL)")
	}
	signedURLTTL := time.Hour
	if raw := strings.TrimSpace(os.Getenv("TENCENT_COS_SIGNED_URL_TTL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < time.Minute || parsed > 24*time.Hour {
			missing = append(missing, "TENCENT_COS_SIGNED_URL_TTL (must be between 1m and 24h)")
		} else {
			signedURLTTL = parsed
		}
	}
	return cosConfiguration{Ready: len(missing) == 0, Missing: missing, Bucket: os.Getenv("TENCENT_COS_BUCKET"), Region: os.Getenv("TENCENT_COS_REGION"), PublicBase: publicBase, SignedURLTTL: signedURLTTL}
}
func (a *App) storageStatus() object {
	c := a.cosConfig()
	return object{"provider": fallbackString(a.store.Setting("storage_provider"), "local"), "local": object{"ready": true, "directory": a.uploadDir}, "cos": object{"ready": c.Ready, "missing": c.Missing, "bucket": c.Bucket, "region": c.Region, "publicBase": c.PublicBase, "signedUrlTTL": c.SignedURLTTL.String()}}
}
func normalizeCOSPublicBase(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimSuffix(u.Path, "/")
	return strings.TrimSuffix(u.String(), "/")
}
func publicCOSURL(key string, c cosConfiguration) string {
	if c.PublicBase != "" {
		return c.PublicBase + "/" + key
	}
	return fmt.Sprintf("https://%s.cos.%s.myqcloud.com/%s", c.Bucket, c.Region, key)
}
func signedCOSURL(key string, c cosConfiguration) (string, error) {
	if !c.Ready || key == "" {
		return publicCOSURL(key, c), nil
	}
	endpoint := strings.TrimSuffix(publicCOSURL("", c), "/")
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	client := cos.NewClient(&cos.BaseURL{BucketURL: u}, &http.Client{Timeout: 10 * time.Second})
	signed, err := client.Object.GetPresignedURL(context.Background(), http.MethodGet, key, os.Getenv("TENCENT_COS_SECRET_ID"), os.Getenv("TENCENT_COS_SECRET_KEY"), c.SignedURLTTL, nil)
	if err != nil {
		return "", err
	}
	return signed.String(), nil
}
func cosClient(c cosConfiguration) (*cos.Client, error) {
	endpoint := fmt.Sprintf("https://%s.cos.%s.myqcloud.com", c.Bucket, c.Region)
	if cosEndpointOverride != "" {
		endpoint = cosEndpointOverride
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	return cos.NewClient(&cos.BaseURL{BucketURL: u}, &http.Client{Timeout: 100 * time.Second, Transport: &cos.AuthorizationTransport{SecretID: os.Getenv("TENCENT_COS_SECRET_ID"), SecretKey: os.Getenv("TENCENT_COS_SECRET_KEY")}}), nil
}

func (a *App) uploadMedia(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 13<<20)
	if err := r.ParseMultipartForm(12 << 20); err != nil {
		msg := "请选择 JPG、PNG 或 WebP 图片"
		if strings.Contains(err.Error(), "request body too large") {
			msg = "图片不能超过 12MB"
		}
		writeJSON(w, 400, object{"error": msg})
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		writeJSON(w, 400, object{"error": "请选择 JPG、PNG 或 WebP 图片"})
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (12<<20)+1))
	if err != nil {
		a.fail(w, err)
		return
	}
	if len(raw) > 12<<20 {
		writeJSON(w, 400, object{"error": "图片不能超过 12MB"})
		return
	}
	mime := http.DetectContentType(raw)
	if mime != "image/jpeg" && mime != "image/png" && mime != "image/webp" {
		writeJSON(w, 400, object{"error": "请选择 JPG、PNG 或 WebP 图片"})
		return
	}
	optimized, width, height, err := optimizeImage(raw, mime)
	if err != nil {
		writeJSON(w, 400, object{"error": "请选择 JPG、PNG 或 WebP 图片"})
		return
	}
	display := normalizeUploadFilename(header.Filename)
	stamp := fmt.Sprintf("%d-%s", time.Now().UnixMilli(), randomHex(5))
	ext := strings.ToLower(filepath.Ext(display))
	if ext == "" {
		ext = ".bin"
	}
	originalName := stamp + ext
	optimizedName := stamp + ".webp"
	provider := fallbackString(a.store.Setting("storage_provider"), "local")
	m := Media{OriginalName: display, Storage: provider, MimeType: mime, Width: width, Height: height, Size: int64(len(optimized))}
	if provider == "cos" {
		cfg := a.cosConfig()
		if !cfg.Ready {
			writeJSON(w, 400, object{"error": "COS 配置缺少：" + strings.Join(cfg.Missing, ", ")})
			return
		}
		client, e := cosClient(cfg)
		if e != nil {
			a.fail(w, e)
			return
		}
		m.OriginalKey = "portfolio/original/" + originalName
		m.OptimizedKey = "portfolio/web/" + optimizedName
		if _, e = client.Object.Put(context.Background(), m.OriginalKey, bytes.NewReader(raw), &cos.ObjectPutOptions{ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{ContentType: mime}}); e != nil {
			a.fail(w, e)
			return
		}
		if _, e = client.Object.Put(context.Background(), m.OptimizedKey, bytes.NewReader(optimized), &cos.ObjectPutOptions{ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{ContentType: "image/webp"}}); e != nil {
			_, _ = client.Object.Delete(context.Background(), m.OriginalKey)
			a.fail(w, e)
			return
		}
		m.OriginalURL = publicCOSURL(m.OriginalKey, cfg)
		m.OptimizedURL = publicCOSURL(m.OptimizedKey, cfg)
	} else {
		m.OriginalKey = "original/" + originalName
		m.OptimizedKey = "web/" + optimizedName
		if err = atomicWriteWithin(a.uploadDir, m.OriginalKey, raw); err != nil {
			a.fail(w, err)
			return
		}
		if err = atomicWriteWithin(a.uploadDir, m.OptimizedKey, optimized); err != nil {
			_ = removeWithin(a.uploadDir, m.OriginalKey)
			a.fail(w, err)
			return
		}
		m.OriginalURL = "/uploads/" + m.OriginalKey
		m.OptimizedURL = "/uploads/" + m.OptimizedKey
	}
	id, err := a.store.CreateMedia(m)
	if err != nil {
		if provider == "local" {
			_ = removeWithin(a.uploadDir, m.OriginalKey)
			_ = removeWithin(a.uploadDir, m.OptimizedKey)
		}
		a.fail(w, err)
		return
	}
	m.ID = id
	a.audit(r, "media_uploaded", "media", fmt.Sprint(id), fmt.Sprintf("%s · %s · %d×%d", display, provider, width, height))
	writeJSON(w, 201, m)
}

func (a *App) removeMedia(w http.ResponseWriter, r *http.Request) {
	id, _ := strconvInt64(r.PathValue("id"))
	m, ok, err := a.store.GetMedia(id)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		writeJSON(w, 404, object{"error": "图片不存在"})
		return
	}
	refs := a.referencedBy(m)
	if len(refs) > 0 {
		writeJSON(w, 409, object{"error": "图片正在被" + strings.Join(refs, "、") + "使用"})
		return
	}
	if m.Storage == "local" {
		for _, k := range []string{m.OriginalKey, m.OptimizedKey} {
			if err = removeWithin(a.uploadDir, k); err != nil && !os.IsNotExist(err) {
				a.fail(w, err)
				return
			}
		}
	} else {
		cfg := a.cosConfig()
		if !cfg.Ready {
			writeJSON(w, 400, object{"error": "COS 配置不完整，无法删除远端文件"})
			return
		}
		client, e := cosClient(cfg)
		if e != nil {
			a.fail(w, e)
			return
		}
		_, _, e = client.Object.DeleteMulti(context.Background(), &cos.ObjectDeleteMultiOptions{Objects: []cos.Object{{Key: m.OriginalKey}, {Key: m.OptimizedKey}}})
		if e != nil {
			a.fail(w, e)
			return
		}
	}
	if err = a.store.DeleteMedia(id); err != nil {
		a.fail(w, err)
		return
	}
	a.audit(r, "media_deleted", "media", fmt.Sprint(id), normalizeUploadFilename(m.OriginalName)+" · "+m.Storage)
	w.WriteHeader(204)
}
func strconvInt64(v string) (int64, error) { var n int64; _, e := fmt.Sscan(v, &n); return n, e }

func normalizeUploadFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\x00", ""))
	hasHan := func(s string) bool {
		for _, r := range s {
			if unicode.Is(unicode.Han, r) {
				return true
			}
		}
		return false
	}
	if hasHan(name) {
		return name
	}
	raw := make([]byte, 0, len(name))
	for _, r := range name {
		if r > 255 {
			return name
		}
		raw = append(raw, byte(r))
	}
	if utf8.Valid(raw) {
		candidate := string(raw)
		if hasHan(candidate) {
			return candidate
		}
	}
	return name
}
func randomHex(n int) string { b := make([]byte, n); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func safePath(root, key string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, key))
	if err != nil {
		return "", err
	}
	if target == rootAbs || !strings.HasPrefix(target, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe media path")
	}
	return target, nil
}
func atomicWriteWithin(root, key string, data []byte) error {
	target, err := safePath(root, key)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".upload-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(name)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, target); err != nil {
		return err
	}
	ok = true
	return nil
}
func removeWithin(root, key string) error {
	target, err := safePath(root, key)
	if err != nil {
		return err
	}
	return os.Remove(target)
}

func optimizeImage(raw []byte, mime string) ([]byte, int, int, error) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, 0, 0, err
	}
	if mime == "image/jpeg" {
		img = orientImage(img, jpegOrientation(raw))
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w > 2400 || h > 2400 {
		scale := 2400.0 / float64(w)
		if h > w {
			scale = 2400.0 / float64(h)
		}
		nw, nh := int(float64(w)*scale+0.5), int(float64(h)*scale+0.5)
		dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
		img = dst
		w, h = nw, nh
	}
	var out bytes.Buffer
	err = gowebp.Encode(&out, img, &gowebp.Options{Lossy: true, Quality: 82, Method: 4})
	if err != nil {
		return nil, 0, 0, err
	}
	return out.Bytes(), w, h, nil
}
func orientImage(src image.Image, o int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 && o <= 8 {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := x, y
			switch o {
			case 2:
				dx = w - 1 - x
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dy = h - 1 - y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	p := 2
	for p+4 <= len(data) {
		if data[p] != 0xff {
			return 1
		}
		marker := data[p+1]
		p += 2
		if marker == 0xd9 || marker == 0xda {
			return 1
		}
		if p+2 > len(data) {
			return 1
		}
		size := int(binary.BigEndian.Uint16(data[p : p+2]))
		if size < 2 || p+size > len(data) {
			return 1
		}
		segment := data[p+2 : p+size]
		p += size
		if marker != 0xe1 || len(segment) < 14 || string(segment[:6]) != "Exif\x00\x00" {
			continue
		}
		t := segment[6:]
		var order binary.ByteOrder
		if string(t[:2]) == "II" {
			order = binary.LittleEndian
		} else if string(t[:2]) == "MM" {
			order = binary.BigEndian
		} else {
			return 1
		}
		ifd := int(order.Uint32(t[4:8]))
		if ifd+2 > len(t) {
			return 1
		}
		count := int(order.Uint16(t[ifd : ifd+2]))
		for i := 0; i < count; i++ {
			off := ifd + 2 + i*12
			if off+12 > len(t) {
				break
			}
			if order.Uint16(t[off:off+2]) == 0x0112 {
				v := int(order.Uint16(t[off+8 : off+10]))
				if v >= 1 && v <= 8 {
					return v
				}
				return 1
			}
		}
	}
	return 1
}

var _ multipart.File
var _ = json.Valid
