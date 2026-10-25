package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0.2.5", "0.2.4", true}, {"0.2.10", "0.2.9", true}, {"0.2.4", "0.2.4", false}, {"0.2.3", "0.2.4", false}, {"1.0.0", "0.9.9", true}} {
		if newer(c.a, c.b) != c.want {
			t.Errorf("newer(%s,%s) != %v", c.a, c.b, c.want)
		}
	}
}

func TestVerifyUpdate(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	old := updatePubKey
	updatePubKey = base64.StdEncoding.EncodeToString(pub)
	defer func() { updatePubKey = old }()
	payload := `{"version":"9.9.9","files":{}}`
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(payload)))
	if p, err := verifyUpdate(payload, sig); err != nil || p.Version != "9.9.9" {
		t.Fatalf("good signature rejected: %v", err)
	}
	if _, err := verifyUpdate(`{"version":"9.9.8","files":{}}`, sig); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

func TestDownloadUpdate(t *testing.T) {
	body := []byte("pretend this is a zip")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	f := updFile{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body)), URLs: []string{srv.URL + "/missing", srv.URL + "/ok"}}
	p, err := downloadUpdate(f)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(p)
	f.SHA256 = hex.EncodeToString(make([]byte, 32))
	if _, err := downloadUpdate(f); err == nil {
		t.Fatal("wrong sha256 accepted")
	}
}
