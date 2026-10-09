package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
)

//go:embed index.html video.html assets server/admin.html server/default-content.json
var webFiles embed.FS

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		healthcheck()
		return
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	b, err := webFiles.ReadFile("server/default-content.json")
	if err != nil {
		return err
	}
	defaultContent = parseObject(b)
	if len(defaultContent) == 0 {
		return fmt.Errorf("default content is empty")
	}
	dataDir := env("DATA_DIR", filepath.Join("server", "data"))
	uploadDir := env("UPLOAD_DIR", filepath.Join(dataDir, "uploads"))
	store, err := openStore(dataDir)
	if err != nil {
		return err
	}
	defer store.Close()
	if password := os.Getenv("ADMIN_PASSWORD"); password == "" {
		log.Print("ADMIN_PASSWORD 未设置，后台不会创建管理员账号。")
	} else {
		existing, ok := store.UserHash("admin")
		if !ok || bcrypt.CompareHashAndPassword([]byte(existing), []byte(password)) != nil {
			hash, e := bcrypt.GenerateFromPassword([]byte(password), 12)
			if e != nil {
				return e
			}
			if e = store.UpsertAdminHash(string(hash)); e != nil {
				return e
			}
		}
	}
	_ = store.CleanSessions()
	app, err := newApp(store, uploadDir)
	if err != nil {
		return err
	}
	port := env("API_PORT", env("PORT", "8787"))
	app.recordStartup(port)
	srv := &http.Server{Addr: ":" + port, Handler: app, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 120 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("Portfolio CMS listening on http://localhost:%s/manage", port)
		errCh <- srv.ListenAndServe()
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	case err = <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}
func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func healthcheck() {
	port := env("API_PORT", env("PORT", "8787"))
	c := http.Client{Timeout: 4 * time.Second}
	r, err := c.Get("http://127.0.0.1:" + port + "/api/health")
	if err != nil || r.StatusCode/100 != 2 {
		os.Exit(1)
	}
	r.Body.Close()
}
func statusInt(v string, fallback int) int {
	n, e := strconv.Atoi(v)
	if e != nil {
		return fallback
	}
	return n
}

var _ fs.FS = webFiles
