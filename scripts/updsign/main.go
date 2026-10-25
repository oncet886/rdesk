// 生成并签名自动升级清单：site/update.json，并把安装包复制到 site/dl/（官网 CDN 下载，GitHub 备用）
// 用法（打包之后、部署官网之前）：go run ./scripts/updsign
// 私钥：~/.config/rdesk/update-signing.key（只在维护者的 Mac 上，不进仓库）
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type file struct {
	Name   string   `json:"name"`
	SHA256 string   `json:"sha256"`
	Size   int64    `json:"size"`
	URLs   []string `json:"urls"`
}

func main() {
	src, err := os.ReadFile("main.go")
	if err != nil {
		log.Fatal("在仓库根目录运行")
	}
	ver := regexp.MustCompile(`const version = "([0-9.]+)"`).FindSubmatch(src)[1]
	v := string(ver)
	home, _ := os.UserHomeDir()
	seed, err := os.ReadFile(filepath.Join(home, ".config/rdesk/update-signing.key"))
	if err != nil {
		log.Fatal(err)
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(seed)))
	if err != nil {
		log.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(s)

	// 测试：UPDSIGN_BASE=下载地址前缀 UPDSIGN_OUT=清单路径（安装包复制到清单旁边，缺的平台跳过）
	base, outPath, dlDir := os.Getenv("UPDSIGN_BASE"), "site/update.json", "site/dl"
	if base != "" {
		outPath = os.Getenv("UPDSIGN_OUT")
		dlDir = filepath.Dir(outPath)
	} else {
		os.RemoveAll(dlDir)
	}
	os.MkdirAll(dlDir, 0o755)
	files := map[string]file{}
	for k, name := range map[string]string{
		"darwin":        "RDesk-Host-macOS-v" + v + ".zip",
		"windows-amd64": "RDesk-Host-Windows-x64-v" + v + ".zip",
		"windows-arm64": "RDesk-Host-Windows-arm64-v" + v + ".zip",
	} {
		b, err := os.ReadFile(filepath.Join("dist", name))
		if err != nil {
			if base != "" {
				continue
			}
			log.Fatalf("缺少 %s（先打包）", name)
		}
		h := sha256.Sum256(b)
		if err := os.WriteFile(filepath.Join(dlDir, name), b, 0o644); err != nil {
			log.Fatal(err)
		}
		urls := []string{
			"https://rdeskapp.com/dl/" + name,
			"https://github.com/oncet886/rdesk/releases/download/v" + v + "/" + name,
		}
		if base != "" {
			urls = []string{base + name}
		}
		files[k] = file{Name: name, SHA256: hex.EncodeToString(h[:]), Size: int64(len(b)), URLs: urls}
	}
	payload, _ := json.Marshal(map[string]any{"version": v, "files": files})
	out, _ := json.MarshalIndent(map[string]string{
		"payload": string(payload),
		"sig":     base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload)),
	}, "", "  ")
	if err := os.WriteFile(outPath, append(out, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s → %s（%d 个安装包已复制到 %s）\n", outPath, v, len(files), dlDir)
}
