package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/image/webp"
)

func loadDefaults(t *testing.T) {
	t.Helper()
	b, err := webFiles.ReadFile("server/default-content.json")
	if err != nil {
		t.Fatal(err)
	}
	defaultContent = parseObject(b)
}

func TestDefaultContentHashIsStable(t *testing.T) {
	loadDefaults(t)
	const want = "d356264168bcdbc1873cc73ac892fdbe7865f28b50a6282d9dcf11d81010bb0b"
	if got := contentHash(defaultContent); got != want {
		t.Fatalf("content hash mismatch\n got %s\nwant %s", got, want)
	}
}

func TestNormalizeLegacyContent(t *testing.T) {
	loadDefaults(t)
	in := object{
		"projects": []any{object{"title": "示例旧视频", "category": "视频", "link": "https://example.com/sample.mp4", "videoUrl": "https://example.com/sample.mp4"}},
		"experiences": []any{
			object{"period": "2020 — 2022", "details": []any{"工作内容：旧数据", "工作成果：已迁移"}},
			object{"detailsMarkdown": "", "details": []any{"不应覆盖"}},
		},
		"caseStudies": []any{object{"details": []any{"项目内容：旧数据"}}},
	}
	out := normalizeContent(in)
	if icon := asObject(out["siteIcon"]); icon == nil || siteIconMediaID(out) != 0 {
		t.Fatalf("legacy content did not receive default site icon settings: %#v", icon)
	}
	p := asObject(asArray(out["projects"])[0])
	if p["type"] != "video" || stringValue(p["vodFileId"]) != "" || p["videoUrl"] != nil || !strings.HasPrefix(stringValue(p["id"]), "work-") {
		t.Fatalf("legacy project not migrated: %#v", p)
	}
	e := asObject(asArray(out["experiences"])[0])
	if e["startDate"] != "2020" || e["endDate"] != "2022" {
		t.Fatalf("legacy experience not migrated: %#v", e)
	}
	if got := stringValue(e["detailsMarkdown"]); got != "- 工作内容：旧数据\n- 工作成果：已迁移" || e["details"] != nil {
		t.Fatalf("legacy experience details not migrated: %#v", e)
	}
	preserved := asObject(asArray(out["experiences"])[1])
	if got := stringValue(preserved["detailsMarkdown"]); got != "" || preserved["details"] != nil {
		t.Fatalf("existing markdown was overwritten: %#v", preserved)
	}
	caseStudy := asObject(asArray(out["caseStudies"])[0])
	if got := stringValue(caseStudy["detailsMarkdown"]); got != "- 项目内容：旧数据" || caseStudy["details"] != nil {
		t.Fatalf("legacy case study details not migrated: %#v", caseStudy)
	}
}

func TestFilenameRecoveryAndSafePath(t *testing.T) {
	mojibake := string([]rune{0x00e4, 0x00b8, 0x00ad, 0x00e6, 0x0096, 0x0087}) + ".jpg"
	if got := normalizeUploadFilename(mojibake); got != "中文.jpg" {
		t.Fatalf("got %q", got)
	}
	if _, err := safePath(t.TempDir(), "../../etc/passwd"); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestOptimizeImageProducesWebP(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2600, 1300))
	for y := 0; y < 1300; y++ {
		for x := 0; x < 2600; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 255), G: uint8(y % 255), B: 80, A: 255})
		}
	}
	var src bytes.Buffer
	if err := png.Encode(&src, img); err != nil {
		t.Fatal(err)
	}
	out, w, h, err := optimizeImage(src.Bytes(), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if w != 2400 || h != 1200 {
		t.Fatalf("unexpected dimensions %dx%d", w, h)
	}
	decoded, err := webp.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != 2400 || decoded.Bounds().Dy() != 1200 {
		t.Fatal("encoded dimensions mismatch")
	}
}

func TestOptimizeImageAppliesEXIFOrientation(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{B: 255, A: 255})
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	tiff := make([]byte, 26)
	copy(tiff[:2], "II")
	binary.LittleEndian.PutUint16(tiff[2:4], 42)
	binary.LittleEndian.PutUint32(tiff[4:8], 8)
	binary.LittleEndian.PutUint16(tiff[8:10], 1)
	binary.LittleEndian.PutUint16(tiff[10:12], 0x0112)
	binary.LittleEndian.PutUint16(tiff[12:14], 3)
	binary.LittleEndian.PutUint32(tiff[14:18], 1)
	binary.LittleEndian.PutUint16(tiff[18:20], 6)
	payload := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xff, 0xe1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	app1 = append(app1, payload...)
	jpegWithEXIF := append([]byte{}, encoded.Bytes()[:2]...)
	jpegWithEXIF = append(jpegWithEXIF, app1...)
	jpegWithEXIF = append(jpegWithEXIF, encoded.Bytes()[2:]...)
	_, w, h, err := optimizeImage(jpegWithEXIF, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if w != 1 || h != 2 {
		t.Fatalf("orientation not applied: %dx%d", w, h)
	}
}

func newTestServer(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	loadDefaults(t)
	root := t.TempDir()
	s, err := openStore(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret-password"), bcrypt.MinCost)
	if err = s.UpsertAdminHash(string(hash)); err != nil {
		t.Fatal(err)
	}
	app, err := newApp(s, filepath.Join(root, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(app)
	t.Cleanup(func() { ts.Close(); s.Close() })
	return ts, s
}
func jsonRequest(t *testing.T, c *http.Client, method, target string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
func loginClient(t *testing.T, base string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	res := jsonRequest(t, c, "POST", base+"/api/admin/login", object{"username": "admin", "password": "secret-password"})
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("login %d: %s", res.StatusCode, b)
	}
	return c
}

func uploadTestImage(t *testing.T, c *http.Client, base, name string, width, height int) Media {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: uint8(128 + (x+y)%128)})
		}
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, img); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("image", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(pngData.Bytes())
	_ = mw.Close()
	req, _ := http.NewRequest("POST", base+"/api/admin/media", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(res.Body)
		t.Fatalf("upload %d: %s", res.StatusCode, data)
	}
	var media Media
	if err = json.NewDecoder(res.Body).Decode(&media); err != nil {
		t.Fatal(err)
	}
	return media
}

func TestAPIWorkflow(t *testing.T) {
	ts, _ := newTestServer(t)
	plain := http.DefaultClient
	res := jsonRequest(t, plain, "GET", ts.URL+"/api/health", nil)
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	if got := res.Header.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Fatalf("unexpected Referrer-Policy: %q", got)
	}
	res.Body.Close()
	res = jsonRequest(t, plain, "GET", ts.URL+"/api/admin/bootstrap", nil)
	if res.StatusCode != 401 {
		t.Fatalf("want 401 got %d", res.StatusCode)
	}
	res.Body.Close()
	c := loginClient(t, ts.URL)
	res = jsonRequest(t, c, "GET", ts.URL+"/api/admin/bootstrap", nil)
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	var bootstrap object
	_ = json.NewDecoder(res.Body).Decode(&bootstrap)
	res.Body.Close()
	content := asObject(bootstrap["content"])
	asObject(content["hero"])["title"] = "集成测试"
	res = jsonRequest(t, c, "PUT", ts.URL+"/api/admin/content", content)
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("save %d %s", res.StatusCode, b)
	}
	res.Body.Close()
	res = jsonRequest(t, c, "POST", ts.URL+"/api/admin/publish", nil)
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("publish %d %s", res.StatusCode, b)
	}
	var published object
	_ = json.NewDecoder(res.Body).Decode(&published)
	res.Body.Close()
	if published["changed"] != true {
		t.Fatalf("expected changed: %#v", published)
	}
	res = jsonRequest(t, c, "POST", ts.URL+"/api/admin/publish", nil)
	var unchanged object
	_ = json.NewDecoder(res.Body).Decode(&unchanged)
	res.Body.Close()
	if unchanged["changed"] != false {
		t.Fatalf("expected duplicate publish skip: %#v", unchanged)
	}
	res = jsonRequest(t, c, "GET", ts.URL+"/", nil)
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if bytes.Contains(page, []byte("application/ld+json")) || !bytes.Contains(page, []byte("noindex,nofollow")) {
		t.Fatal("privacy-safe SEO defaults not applied")
	}
}

func TestMediaUploadAndDelete(t *testing.T) {
	ts, _ := newTestServer(t)
	c := loginClient(t, ts.URL)
	img := image.NewNRGBA(image.Rect(0, 0, 32, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{200, 20, 30, 255})
		}
	}
	var pngData bytes.Buffer
	_ = png.Encode(&pngData, img)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("image", "测试.png")
	_, _ = part.Write(pngData.Bytes())
	_ = mw.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/api/admin/media", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 201 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("upload %d: %s", res.StatusCode, b)
	}
	var m Media
	_ = json.NewDecoder(res.Body).Decode(&m)
	res.Body.Close()
	if m.OriginalName != "测试.png" || m.Width != 32 || m.Height != 16 || !strings.HasSuffix(m.OptimizedURL, ".webp") {
		t.Fatalf("bad media: %#v", m)
	}
	u, _ := url.Parse(ts.URL + m.OptimizedURL)
	res, err = c.Get(u.String())
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("optimized image unavailable: %v %v", res, err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if _, err = webp.Decode(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	res = jsonRequest(t, c, "DELETE", ts.URL+"/api/admin/media/"+fmt.Sprint(m.ID), nil)
	if res.StatusCode != 204 {
		t.Fatalf("delete %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestSiteIconPublishManifestAndGeneratedSizes(t *testing.T) {
	ts, _ := newTestServer(t)
	c := loginClient(t, ts.URL)
	media := uploadTestImage(t, c, ts.URL, "site-icon.png", 512, 512)

	res := jsonRequest(t, c, "GET", ts.URL+"/api/admin/bootstrap", nil)
	var bootstrap object
	_ = json.NewDecoder(res.Body).Decode(&bootstrap)
	res.Body.Close()
	content := asObject(bootstrap["content"])
	asObject(content["siteIcon"])["mediaId"] = media.ID
	res = jsonRequest(t, c, "PUT", ts.URL+"/api/admin/content", content)
	if res.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(res.Body)
		t.Fatalf("save icon draft %d: %s", res.StatusCode, data)
	}
	res.Body.Close()

	res = jsonRequest(t, http.DefaultClient, "GET", ts.URL+"/", nil)
	beforePublish, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if bytes.Contains(beforePublish, []byte("/icons/site-32.png")) {
		t.Fatal("draft icon leaked into published page")
	}
	res = jsonRequest(t, http.DefaultClient, "GET", ts.URL+"/icons/site-32.png", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("draft icon endpoint returned %d", res.StatusCode)
	}
	res.Body.Close()

	res = jsonRequest(t, c, "POST", ts.URL+"/api/admin/publish", nil)
	if res.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(res.Body)
		t.Fatalf("publish icon %d: %s", res.StatusCode, data)
	}
	var published object
	_ = json.NewDecoder(res.Body).Decode(&published)
	res.Body.Close()
	version := stringValue(published["hash"])[:12]

	for _, path := range []string{"/", "/video.html"} {
		res = jsonRequest(t, http.DefaultClient, "GET", ts.URL+path, nil)
		page, _ := io.ReadAll(res.Body)
		res.Body.Close()
		for _, want := range []string{
			"rel=\"icon\" type=\"image/png\" sizes=\"32x32\" href=\"/icons/site-32.png?v=" + version,
			"rel=\"apple-touch-icon\" sizes=\"180x180\" href=\"/icons/site-180.png?v=" + version,
			"rel=\"manifest\" href=\"/site.webmanifest?v=" + version,
		} {
			if !bytes.Contains(page, []byte(want)) {
				t.Fatalf("%s missing %q", path, want)
			}
		}
	}

	res = jsonRequest(t, http.DefaultClient, "GET", ts.URL+"/site.webmanifest?v="+version, nil)
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/manifest+json") {
		t.Fatalf("unexpected manifest content type: %q", got)
	}
	var manifest object
	_ = json.NewDecoder(res.Body).Decode(&manifest)
	res.Body.Close()
	if manifest["display"] != "browser" || manifest["start_url"] != "/" || len(asArray(manifest["icons"])) != 2 {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	for _, raw := range asArray(manifest["icons"]) {
		icon := asObject(raw)
		if icon["purpose"] != "any" || icon["type"] != "image/png" {
			t.Fatalf("unexpected manifest icon: %#v", icon)
		}
	}

	for _, size := range []int{32, 180, 192, 512} {
		target := fmt.Sprintf("%s/icons/site-%d.png?v=%s", ts.URL, size, version)
		res = jsonRequest(t, http.DefaultClient, "GET", target, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("icon %d returned %d", size, res.StatusCode)
		}
		if res.Header.Get("Content-Type") != "image/png" || !strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
			t.Fatalf("icon %d headers: %#v", size, res.Header)
		}
		decoded, err := png.Decode(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatalf("decode icon %d: %v", size, err)
		}
		if decoded.Bounds().Dx() != size || decoded.Bounds().Dy() != size {
			t.Fatalf("icon %d has bounds %v", size, decoded.Bounds())
		}
		_, _, _, alpha := decoded.At(size/2, size/2).RGBA()
		if alpha == 0xffff {
			t.Fatalf("icon %d lost source transparency", size)
		}
	}

	noRedirect := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := noRedirect.Get(ts.URL + "/favicon.ico")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusTemporaryRedirect || !strings.Contains(res.Header.Get("Location"), "/icons/site-32.png?v="+version) {
		t.Fatalf("favicon redirect: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	res.Body.Close()

	res = jsonRequest(t, c, "DELETE", ts.URL+"/api/admin/media/"+fmt.Sprint(media.ID), nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("referenced icon deletion returned %d", res.StatusCode)
	}
	res.Body.Close()

	asObject(content["siteIcon"])["mediaId"] = 0
	res = jsonRequest(t, c, "PUT", ts.URL+"/api/admin/content", content)
	res.Body.Close()
	res = jsonRequest(t, c, "POST", ts.URL+"/api/admin/publish", nil)
	res.Body.Close()
	res = jsonRequest(t, c, "DELETE", ts.URL+"/api/admin/media/"+fmt.Sprint(media.ID), nil)
	if res.StatusCode != http.StatusNoContent {
		data, _ := io.ReadAll(res.Body)
		t.Fatalf("unreferenced icon deletion %d: %s", res.StatusCode, data)
	}
	res.Body.Close()
}

func TestSiteIconPublishValidation(t *testing.T) {
	ts, _ := newTestServer(t)
	c := loginClient(t, ts.URL)
	media := uploadTestImage(t, c, ts.URL, "small-icon.png", 256, 256)
	res := jsonRequest(t, c, "GET", ts.URL+"/api/admin/bootstrap", nil)
	var bootstrap object
	_ = json.NewDecoder(res.Body).Decode(&bootstrap)
	res.Body.Close()
	content := asObject(bootstrap["content"])
	asObject(content["siteIcon"])["mediaId"] = media.ID
	res = jsonRequest(t, c, "PUT", ts.URL+"/api/admin/content", content)
	res.Body.Close()
	res = jsonRequest(t, c, "POST", ts.URL+"/api/admin/publish", nil)
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest || !bytes.Contains(data, []byte("512×512")) {
		t.Fatalf("invalid icon publish %d: %s", res.StatusCode, data)
	}
	res = jsonRequest(t, http.DefaultClient, "GET", ts.URL+"/", nil)
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if bytes.Contains(page, []byte("/icons/site-32.png")) {
		t.Fatal("invalid icon changed published page")
	}

	asObject(content["siteIcon"])["mediaId"] = int64(999999)
	res = jsonRequest(t, c, "PUT", ts.URL+"/api/admin/content", content)
	res.Body.Close()
	res = jsonRequest(t, c, "POST", ts.URL+"/api/admin/publish", nil)
	data, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest || !bytes.Contains(data, []byte("不在媒体库")) {
		t.Fatalf("missing icon publish %d: %s", res.StatusCode, data)
	}
}

func TestCOSMediaUploadAndDelete(t *testing.T) {
	var methods []string
	cosServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.RequestURI())
		if r.Method == http.MethodPut {
			data, _ := io.ReadAll(r.Body)
			w.Header().Set("x-cos-hash-crc64ecma", strconv.FormatUint(crc64.Checksum(data, crc64.MakeTable(crc64.ECMA)), 10))
		}
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, `<DeleteResult></DeleteResult>`)
		}
	}))
	defer cosServer.Close()
	cosEndpointOverride = cosServer.URL
	defer func() { cosEndpointOverride = "" }()
	t.Setenv("TENCENT_COS_SECRET_ID", "test-id")
	t.Setenv("TENCENT_COS_SECRET_KEY", "test-key")
	t.Setenv("TENCENT_COS_BUCKET", "bucket-123")
	t.Setenv("TENCENT_COS_REGION", "test-region")
	t.Setenv("TENCENT_COS_CUSTOM_DOMAIN", "media.example.com/")
	ts, store := newTestServer(t)
	if err := store.SetSetting("storage_provider", "cos"); err != nil {
		t.Fatal(err)
	}
	c := loginClient(t, ts.URL)
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	var pngData bytes.Buffer
	_ = png.Encode(&pngData, img)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("image", "cos.png")
	_, _ = part.Write(pngData.Bytes())
	_ = mw.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/api/admin/media", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 201 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("COS upload %d: %s", res.StatusCode, b)
	}
	var m Media
	_ = json.NewDecoder(res.Body).Decode(&m)
	res.Body.Close()
	if !strings.HasPrefix(m.OptimizedURL, "https://media.example.com/portfolio/web/") {
		t.Fatalf("custom COS domain not applied: %q", m.OptimizedURL)
	}
	res = jsonRequest(t, c, "DELETE", ts.URL+"/api/admin/media/"+fmt.Sprint(m.ID), nil)
	if res.StatusCode != 204 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("COS delete %d: %s", res.StatusCode, b)
	}
	res.Body.Close()
	if len(methods) != 3 || !strings.HasPrefix(methods[0], "PUT /portfolio%2Foriginal%2F") || !strings.HasPrefix(methods[1], "PUT /portfolio%2Fweb%2F") || !strings.HasPrefix(methods[2], "POST /?delete") {
		t.Fatalf("unexpected COS calls: %#v", methods)
	}
}

func TestSiteIconReadsOriginalFromCOS(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	source.SetNRGBA(256, 256, color.NRGBA{R: 220, G: 40, B: 60, A: 120})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	cosServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected COS method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(encoded.Bytes())
	}))
	defer cosServer.Close()
	cosEndpointOverride = cosServer.URL
	defer func() { cosEndpointOverride = "" }()
	t.Setenv("TENCENT_COS_SECRET_ID", "test-id")
	t.Setenv("TENCENT_COS_SECRET_KEY", "test-key")
	t.Setenv("TENCENT_COS_BUCKET", "bucket-123")
	t.Setenv("TENCENT_COS_REGION", "test-region")

	loadDefaults(t)
	root := t.TempDir()
	store, err := openStore(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app, err := newApp(store, filepath.Join(root, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := app.readIconSource(context.Background(), Media{Storage: "cos", OriginalKey: "portfolio/original/icon.png"})
	if err != nil {
		t.Fatal(err)
	}
	generated, err := resizeIcon(raw, "image/png", 192)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(generated))
	if err != nil || decoded.Bounds().Dx() != 192 {
		t.Fatalf("generated COS icon is invalid: %v %v", decoded, err)
	}
}

func TestCOSCustomDomainRewritesPublishedMedia(t *testing.T) {
	t.Setenv("TENCENT_COS_SECRET_ID", "test-id")
	t.Setenv("TENCENT_COS_SECRET_KEY", "test-key")
	t.Setenv("TENCENT_COS_BUCKET", "bucket-123")
	t.Setenv("TENCENT_COS_REGION", "ap-guangzhou")
	t.Setenv("TENCENT_COS_CUSTOM_DOMAIN", "https://media.example.com")
	t.Setenv("TENCENT_COS_SIGNED_URL_TTL", "15m")
	ts, store := newTestServer(t)
	storedURL := "https://bucket-123.cos.ap-guangzhou.myqcloud.com/portfolio/web/old.webp"
	_, err := store.CreateMedia(Media{
		OriginalName: "old.png",
		Storage:      "cos",
		OriginalURL:  "https://bucket-123.cos.ap-guangzhou.myqcloud.com/portfolio/original/old.png",
		OptimizedURL: storedURL,
		OriginalKey:  "portfolio/original/old.png",
		OptimizedKey: "portfolio/web/old.webp",
		MimeType:     "image/png",
	})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := store.GetDocument("draft")
	if err != nil {
		t.Fatal(err)
	}
	asObject(draft.Content["profile"])["avatarUrl"] = "https://previous-media.example.com/portfolio/web/old.webp"
	if _, err = store.SaveDraft(draft.Content); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Publish("test"); err != nil {
		t.Fatal(err)
	}
	res := jsonRequest(t, http.DefaultClient, "GET", ts.URL+"/api/site", nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatal(res.Status)
	}
	if got := res.Header.Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("unexpected cache control: %q", got)
	}
	var content object
	if err = json.NewDecoder(res.Body).Decode(&content); err != nil {
		t.Fatal(err)
	}
	got := stringValue(asObject(content["profile"])["avatarUrl"])
	signed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Scheme != "https" || signed.Host != "media.example.com" || signed.Path != "/portfolio/web/old.webp" || signed.Query().Get("q-ak") != "test-id" || signed.Query().Get("q-signature") == "" {
		t.Fatalf("historical COS URL was not signed correctly: %q", got)
	}

	c := loginClient(t, ts.URL)
	adminRes := jsonRequest(t, c, "GET", ts.URL+"/api/admin/bootstrap", nil)
	defer adminRes.Body.Close()
	var payload object
	if err = json.NewDecoder(adminRes.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if stable := stringValue(asObject(asObject(payload["content"])["profile"])["avatarUrl"]); stable != "https://previous-media.example.com/portfolio/web/old.webp" {
		t.Fatalf("admin content persisted an ephemeral URL: %q", stable)
	}
	items := asArray(payload["media"])
	if len(items) != 1 {
		t.Fatalf("unexpected media payload: %#v", items)
	}
	item := asObject(items[0])
	if display := stringValue(item["optimizedDisplayUrl"]); !strings.Contains(display, "q-signature=") {
		t.Fatalf("admin media preview is not signed: %q", display)
	}
}

func TestVODPlayerSignature(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	token, expiresAt, err := createVODPlayerSignature(1250000000, "file-123", "playback-secret", now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if expiresAt != now.Unix()+600 {
		t.Fatalf("unexpected expiry: %d", expiresAt)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("invalid JWT: %q", token)
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var header, payload object
	if json.Unmarshal(headerBytes, &header) != nil || json.Unmarshal(payloadBytes, &payload) != nil {
		t.Fatal("unable to decode JWT JSON")
	}
	if header["alg"] != "HS256" || header["typ"] != "JWT" || payload["fileId"] != "file-123" || payload["appId"] != float64(1250000000) {
		t.Fatalf("unexpected JWT claims: header=%#v payload=%#v", header, payload)
	}
	if contentInfo := asObject(payload["contentInfo"]); contentInfo["audioVideoType"] != "Original" {
		t.Fatalf("unexpected contentInfo: %#v", contentInfo)
	}
	mac := hmac.New(sha256.New, []byte("playback-secret"))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	wantSignature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[2]), []byte(wantSignature)) {
		t.Fatal("JWT signature mismatch")
	}
}

func TestVODPlaybackAPI(t *testing.T) {
	t.Setenv("TENCENT_VOD_APP_ID", "1250000000")
	t.Setenv("TENCENT_VOD_PLAYBACK_KEY", "do-not-leak-this-secret")
	t.Setenv("TENCENT_VOD_LICENSE_URL", "https://license.example.test/vod")
	t.Setenv("TENCENT_VOD_SIGNATURE_TTL", "10m")
	ts, store := newTestServer(t)
	draft, err := store.GetDocument("draft")
	if err != nil {
		t.Fatal(err)
	}
	draft.Content["projects"] = []any{
		object{"id": "published-video", "type": "video", "title": "Published", "vodFileId": "file-123", "published": true},
		object{"id": "unpublished-video", "type": "video", "title": "Hidden", "vodFileId": "file-hidden", "published": false},
		object{"id": "missing-file", "type": "video", "title": "Missing", "vodFileId": "", "published": true},
		object{"id": "photo", "type": "photo", "title": "Photo", "externalUrl": "https://example.com", "published": true},
	}
	if _, err = store.SaveDraft(draft.Content); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Publish("test"); err != nil {
		t.Fatal(err)
	}

	res := jsonRequest(t, http.DefaultClient, "GET", ts.URL+"/api/videos/published-video/playback", nil)
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("playback %d: %s", res.StatusCode, body)
	}
	if res.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("unexpected cache header: %q", res.Header.Get("Cache-Control"))
	}
	var playback object
	if err = json.NewDecoder(res.Body).Decode(&playback); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if playback["appId"] != "1250000000" || playback["fileId"] != "file-123" || playback["licenseUrl"] != "https://license.example.test/vod" || stringValue(playback["psign"]) == "" {
		t.Fatalf("unexpected playback payload: %#v", playback)
	}
	encoded, _ := json.Marshal(playback)
	if bytes.Contains(encoded, []byte("do-not-leak-this-secret")) {
		t.Fatal("playback key leaked in playback response")
	}

	for _, id := range []string{"unknown", "unpublished-video", "missing-file", "photo"} {
		res = jsonRequest(t, http.DefaultClient, "GET", ts.URL+"/api/videos/"+id+"/playback", nil)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: want 404, got %d", id, res.StatusCode)
		}
		if res.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s: missing private cache policy", id)
		}
		res.Body.Close()
	}
	if issues := projectValidationIssues(draft.Content); len(issues) != 1 || !strings.Contains(issues[0], "VOD FileID") {
		t.Fatalf("unexpected VOD publish validation: %#v", issues)
	}

	res = jsonRequest(t, http.DefaultClient, "GET", ts.URL+"/api/site", nil)
	siteBody, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if bytes.Contains(siteBody, []byte("do-not-leak-this-secret")) || bytes.Contains(siteBody, []byte("videoUrl")) {
		t.Fatal("secret or legacy videoUrl leaked in public site payload")
	}

	c := loginClient(t, ts.URL)
	res = jsonRequest(t, c, "GET", ts.URL+"/api/admin/bootstrap", nil)
	adminBody, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if bytes.Contains(adminBody, []byte("do-not-leak-this-secret")) {
		t.Fatal("playback key leaked in admin bootstrap")
	}

	res = jsonRequest(t, http.DefaultClient, "GET", ts.URL+"/video.html?id=published-video", nil)
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Contains(page, []byte("tcplayer.v5.3.4.min.js")) || !bytes.Contains(page, []byte("tcplayer.min.css")) || !bytes.Contains(page, []byte("video_detail.js?v=")) || bytes.Contains(page, []byte("do-not-leak-this-secret")) {
		t.Fatal("video page does not load TCPlayer safely")
	}
	script, err := webFiles.ReadFile("assets/js/video_detail.js")
	if err != nil {
		t.Fatal(err)
	}
	styles, err := webFiles.ReadFile("assets/css/video-detail.css")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(styles, []byte(".player-error[hidden]{display:none}")) {
		t.Fatal("hidden player error overlay can override the video")
	}
	for _, option := range []string{"appID: playback.appId", "fileID: playback.fileId", "psign: playback.psign", "licenseUrl: playback.licenseUrl"} {
		if !bytes.Contains(script, []byte(option)) {
			t.Fatalf("TCPlayer initialization is missing %s", option)
		}
	}
	res = jsonRequest(t, http.DefaultClient, "GET", ts.URL+"/sitemap.xml", nil)
	sitemap, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Contains(sitemap, []byte("published-video")) || bytes.Contains(sitemap, []byte("unpublished-video")) || bytes.Contains(sitemap, []byte("missing-file")) {
		t.Fatalf("unexpected VOD sitemap: %s", sitemap)
	}
}

func TestVODPlaybackAPIRejectsIncompleteConfiguration(t *testing.T) {
	t.Setenv("TENCENT_VOD_APP_ID", "")
	t.Setenv("TENCENT_VOD_PLAYBACK_KEY", "")
	t.Setenv("TENCENT_VOD_LICENSE_URL", "")
	ts, store := newTestServer(t)
	draft, err := store.GetDocument("draft")
	if err != nil {
		t.Fatal(err)
	}
	draft.Content["projects"] = []any{object{"id": "video", "type": "video", "title": "Video", "vodFileId": "file-123", "published": true}}
	if _, err = store.SaveDraft(draft.Content); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Publish("test"); err != nil {
		t.Fatal(err)
	}
	res := jsonRequest(t, http.DefaultClient, "GET", ts.URL+"/api/videos/video/playback", nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", res.StatusCode)
	}
}

func TestLegacyVideoTableMigratesFileID(t *testing.T) {
	loadDefaults(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "portfolio.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE videos (id INTEGER PRIMARY KEY AUTOINCREMENT,title TEXT NOT NULL,description TEXT DEFAULT '',cover_url TEXT DEFAULT '',file_id TEXT DEFAULT '',playback_url TEXT DEFAULT '',sort_order INTEGER DEFAULT 0,published INTEGER DEFAULT 1,created_at TEXT DEFAULT CURRENT_TIMESTAMP,updated_at TEXT DEFAULT CURRENT_TIMESTAMP);
		CREATE TABLE site_content (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO site_content(key,value) VALUES ('main','{"projects":[{"title":"Legacy VOD","category":"视频","videoUrl":"https://legacy.example/video.mp4"}],"experiences":[]}');
		INSERT INTO videos(title,file_id,playback_url) VALUES ('Legacy VOD','legacy-file-id','https://legacy.example/video.mp4')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	document, err := store.GetDocument("published")
	if err != nil {
		t.Fatal(err)
	}
	var migrated object
	for _, raw := range asArray(document.Content["projects"]) {
		item := asObject(raw)
		if item["title"] == "Legacy VOD" {
			migrated = item
			break
		}
	}
	if migrated == nil || migrated["vodFileId"] != "legacy-file-id" || migrated["videoUrl"] != nil {
		t.Fatalf("legacy video was not migrated safely: %#v", migrated)
	}
}

func TestClientDiagnosticsAreStoredAndSanitized(t *testing.T) {
	ts, store := newTestServer(t)
	body := object{
		"type":        "vod_error",
		"projectId":   "project-123",
		"stage":       "tcplayer",
		"code":        "1001",
		"message":     "request failed psign=super-secret-token",
		"resourceUrl": "https://vod.example/video.m3u8?psign=url-secret&q-signature=cos-secret",
		"pageUrl":     ts.URL + "/video.html?id=project-123",
		"requestId":   "playback-request-123",
	}
	encoded, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/client-events", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "diagnostic-test-browser")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", res.StatusCode)
	}
	logs, err := store.ListLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	var entry AdminLog
	var access AdminLog
	for _, item := range logs {
		if item.Action == "vod_client_error" {
			entry = item
		}
		if item.Action == "http_request" && item.Path == "/api/client-events" {
			access = item
		}
	}
	if entry.Action != "vod_client_error" || entry.TargetType != "video" || entry.TargetID != "project-123" || !strings.Contains(entry.Detail, "diagnostic-test-browser") || !strings.Contains(entry.Detail, "https://vod.example/video.m3u8") {
		t.Fatalf("unexpected diagnostic entry: %#v", entry)
	}
	if !strings.Contains(entry.Detail, "playback-request-123") {
		t.Fatalf("diagnostic does not contain related request ID: %#v", entry)
	}
	if strings.Contains(entry.Detail, "super-secret") || strings.Contains(entry.Detail, "url-secret") || strings.Contains(entry.Detail, "cos-secret") || strings.Contains(entry.Detail, "?") {
		t.Fatalf("diagnostic leaked a secret or URL query: %q", entry.Detail)
	}
	if access.StatusCode != http.StatusNoContent || access.Method != http.MethodPost || access.RequestID == "" || access.ResponseBytes != 0 || access.UserAgent != "diagnostic-test-browser" {
		t.Fatalf("unexpected access entry: %#v", access)
	}
	if got := res.Header.Get("X-Request-ID"); got == "" || got != access.RequestID {
		t.Fatalf("response/access request IDs do not match: response=%q access=%q", got, access.RequestID)
	}

	res = jsonRequest(t, http.DefaultClient, http.MethodPost, ts.URL+"/api/client-events", object{"type": "unsupported"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for unsupported event, got %d", res.StatusCode)
	}
}

func TestAccessLogSanitizesQueriesAndRecordsFailures(t *testing.T) {
	ts, store := newTestServer(t)
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/missing?token=must-not-be-logged", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Referer", "https://example.test/source?secret=value")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	logs, err := store.ListLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	var access AdminLog
	for _, item := range logs {
		if item.Action == "http_request" && item.Path == "/missing" {
			access = item
			break
		}
	}
	if access.StatusCode != http.StatusNotFound || access.RequestID == "" || access.Method != http.MethodGet {
		t.Fatalf("missing failed request access log: %#v", access)
	}
	if strings.Contains(access.Detail, "must-not-be-logged") || strings.Contains(access.Referer, "secret") || strings.Contains(access.Referer, "?") {
		t.Fatalf("access log leaked query data: %#v", access)
	}
}
