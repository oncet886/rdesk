package main

import (
	"crypto/tls"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// 中继服务器自动申请 HTTPS 证书：80 端口回应 Let's Encrypt 的验证（其余跳到 https），443 端口提供服务
func serveAutocert(h http.Handler, domain, dir string) error {
	if dir == "" {
		dir = filepath.Join(filepath.Dir(configPath()), "certs")
	}
	os.MkdirAll(dir, 0o700)
	m := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(domain), Cache: autocert.DirCache(dir)}
	go func() { log.Fatal(http.ListenAndServe(":80", m.HTTPHandler(nil))) }()
	srv := &http.Server{Addr: ":443", Handler: h, ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{GetCertificate: m.GetCertificate, NextProtos: []string{"http/1.1", "acme-tls/1"}}}
	log.Printf("中继服务器已启动 https://%s", domain)
	return srv.ListenAndServeTLS("", "")
}
