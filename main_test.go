package main

import (
	"bytes"
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
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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
	const want = "4a032584ad4957530131d8a96fd1b3a497a45f377a39be4489ee5c359f1f97f4"
	if got := contentHash(defaultContent); got != want {
		t.Fatalf("content hash mismatch\n got %s\nwant %s", got, want)
	}
}

func TestNormalizeLegacyContent(t *testing.T) {
	loadDefaults(t)
	in := object{"projects": []any{object{"title": "示例旧视频", "category": "视频", "link": "https://example.com/sample.mp4"}}, "experiences": []any{object{"period": "2020 — 2022"}}}
	out := normalizeContent(in)
	p := asObject(asArray(out["projects"])[0])
	if p["type"] != "video" || p["videoUrl"] == "" || !strings.HasPrefix(stringValue(p["id"]), "work-") {
		t.Fatalf("legacy project not migrated: %#v", p)
	}
	e := asObject(asArray(out["experiences"])[0])
	if e["startDate"] != "2020" || e["endDate"] != "2022" {
		t.Fatalf("legacy experience not migrated: %#v", e)
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

func TestAPIWorkflow(t *testing.T) {
	ts, _ := newTestServer(t)
	plain := http.DefaultClient
	res := jsonRequest(t, plain, "GET", ts.URL+"/api/health", nil)
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
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
