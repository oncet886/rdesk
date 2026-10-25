//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 用 dist 里正式签名的 App 测替换；改成临时签名的包必须被拒绝，原 App 不动
func TestReplaceApp(t *testing.T) {
	signed := "dist/RDesk Host.app"
	if _, err := os.Stat(signed); err != nil {
		t.Skip("先运行 scripts/package-mac.sh")
	}
	dir := t.TempDir()
	run := func(name string, args ...string) {
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v %s", name, args, err, out)
		}
	}
	app := filepath.Join(dir, "Applications", "RDesk Host.app")
	os.MkdirAll(filepath.Dir(app), 0o755)
	run("ditto", signed, app)
	good := filepath.Join(dir, "good.zip")
	run("ditto", "-c", "-k", "--keepParent", signed, good)
	if err := replaceApp(app, good); err != nil {
		t.Fatalf("signed update rejected: %v", err)
	}
	if _, err := signer(app); err != nil {
		t.Fatal(err)
	}
	// 冒充的包：同名 App，但只有临时签名
	fake := filepath.Join(dir, "fake", "RDesk Host.app")
	os.MkdirAll(filepath.Dir(fake), 0o755)
	run("ditto", signed, fake)
	run("codesign", "--force", "--deep", "-s", "-", fake)
	bad := filepath.Join(dir, "bad.zip")
	run("ditto", "-c", "-k", "--keepParent", fake, bad)
	if err := replaceApp(app, bad); err == nil {
		t.Fatal("ad-hoc signed update accepted")
	} else {
		t.Log("rejected as expected:", err)
	}
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		t.Fatalf("original app damaged: %s", out)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "Applications", ".rdesk-update-*")); len(left) > 0 {
		t.Fatalf("temp dirs left: %v", left)
	}
}
