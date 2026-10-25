package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"
)

// 同一个端口：明文 HTTP（已发布的 App）和 TLS（浏览器）都要能连
func TestSniffListener(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "x"}, DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sl := &sniffListener{Listener: ln, tls: &tls.Config{Certificates: []tls.Certificate{cert}}, ch: make(chan net.Conn)}
	go http.Serve(sl, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			w.Write([]byte("tls"))
		} else {
			w.Write([]byte("plain"))
		}
	}))
	// 一个只连不发数据的慢连接，不能挡住后面的人
	idle, _ := net.Dial("tcp", ln.Addr().String())
	defer idle.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	get := func(c *http.Client, u string) string {
		r, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return string(b)
	}
	cl := &http.Client{Timeout: 3 * time.Second}
	if got := get(cl, "http://127.0.0.1:"+itoa(port)+"/"); got != "plain" {
		t.Fatalf("plain: %q", got)
	}
	pool := x509.NewCertPool()
	c, _ := x509.ParseCertificate(der)
	pool.AddCert(c)
	tcl := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	if got := get(tcl, "https://localhost:"+itoa(port)+"/"); got != "tls" {
		t.Fatalf("tls: %q", got)
	}
}

func itoa(n int) string { return big.NewInt(int64(n)).String() }
