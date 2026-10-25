package main

// 自动升级：定时读 rdeskapp.com/update.json（ed25519 签名，公钥写死在程序里），有新版本就在没人连着时
// 下载、校验 SHA-256、替换程序并重启。Mac 版另外核对新程序的苹果签名必须是我们的开发者证书。
// 发布：scripts/updsign 生成并签名 update.json（私钥在维护者 Mac 的 ~/.config/rdesk/update-signing.key）。

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var updatePubKey = "4PKV6Al1V3q0NkorB6JbxM3knD9qWe6EvmFePwqB2J4=" // 测试里会换成临时公钥

func siteDownload() string {
	if langCol < 0 || langCol == 1 {
		return "https://rdeskapp.com/#download"
	}
	return "https://rdeskapp.com/en/#download"
}

func updateURL() string {
	if u := os.Getenv("RDESK_UPDATE_URL"); u != "" { // 测试用
		return u
	}
	return "https://rdeskapp.com/update.json"
}

// update.json = {"payload": "<下面这个结构的 JSON 文本>", "sig": "<对 payload 文本的 ed25519 签名>"}
type updPayload struct {
	Version string             `json:"version"`
	Files   map[string]updFile `json:"files"` // 键：darwin / windows-amd64 / windows-arm64
}

type updFile struct {
	Name   string   `json:"name"`
	SHA256 string   `json:"sha256"`
	Size   int64    `json:"size"`
	URLs   []string `json:"urls"` // 按顺序尝试（官网 CDN、GitHub）
}

type updState struct {
	mu     sync.Mutex
	State  string `json:"state"`            // checking / latest / available / waiting / downloading / error
	Latest string `json:"latest,omitempty"` // 发现的新版本号
	Err    string `json:"err,omitempty"`
	busy   bool
}

var upd updState

func (u *updState) set(state, latest, err string) {
	u.mu.Lock()
	u.State, u.Latest, u.Err = state, latest, err
	u.mu.Unlock()
}

type updInfo struct {
	Auto    bool   `json:"auto"`
	Can     bool   `json:"can"` // 这个平台 / 安装方式能自动替换
	State   string `json:"state,omitempty"`
	Latest  string `json:"latest,omitempty"`
	Err     string `json:"err,omitempty"`
	Version string `json:"version"`
}

func updSnapshot() updInfo {
	upd.mu.Lock()
	defer upd.mu.Unlock()
	return updInfo{Auto: !loadConfig().NoAutoUpdate, Can: updateSupported(), State: upd.State, Latest: upd.Latest, Err: upd.Err, Version: version}
}

func setAutoUpdate(on bool) {
	cfg := loadConfig()
	cfg.NoAutoUpdate = !on
	saveConfig(cfg)
}

// 启动后 1 分钟检查一次，之后每 6 小时；有新版本时等到没人连着再装
func updateLoop() {
	updateCleanup()
	time.Sleep(time.Minute)
	for {
		if !loadConfig().NoAutoUpdate {
			runUpdate(false)
		}
		time.Sleep(6 * time.Hour)
	}
}

// 检查并安装。now = 用户手动点了「检查更新」：有人连着也立刻装
func runUpdate(now bool) {
	upd.mu.Lock()
	if upd.busy {
		upd.mu.Unlock()
		return
	}
	upd.busy = true
	upd.mu.Unlock()
	defer func() { upd.mu.Lock(); upd.busy = false; upd.mu.Unlock() }()

	upd.set("checking", "", "")
	p, err := fetchUpdate()
	if err != nil {
		log.Printf(L("检查更新失败: %v"), err)
		upd.set("error", "", err.Error())
		return
	}
	if !newer(p.Version, version) {
		upd.set("latest", "", "")
		return
	}
	f, ok := p.Files[updateKey()]
	if !ok || !updateSupported() {
		upd.set("available", p.Version, "")
		return
	}
	for !now && viewerActive() { // 正在被远程控制：不打断，断开后再装
		upd.set("waiting", p.Version, "")
		time.Sleep(time.Minute)
	}
	upd.set("downloading", p.Version, "")
	log.Printf(L("⬆️ 发现新版本 %s，正在下载…"), p.Version)
	path, err := downloadUpdate(f)
	if err == nil {
		defer os.Remove(path)
		log.Printf(L("⬆️ 正在安装 %s，完成后自动重启…"), p.Version)
		err = applyUpdate(path) // 成功的话不会返回（程序已重启）
	}
	log.Printf(L("更新失败: %v"), err)
	upd.set("error", p.Version, err.Error())
}

func viewerActive() bool {
	diag.mu.Lock()
	defer diag.mu.Unlock()
	return diag.active
}

func fetchUpdate() (*updPayload, error) {
	c := &http.Client{Timeout: 20 * time.Second}
	r, err := c.Get(updateURL())
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("update.json: %s", r.Status)
	}
	var env struct{ Payload, Sig string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&env); err != nil {
		return nil, err
	}
	return verifyUpdate(env.Payload, env.Sig)
}

func verifyUpdate(payload, sig string) (*updPayload, error) {
	pub, _ := base64.StdEncoding.DecodeString(updatePubKey)
	s, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || !ed25519.Verify(pub, []byte(payload), s) {
		return nil, errors.New("update.json signature mismatch")
	}
	var p updPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// 版本号按数字逐段比较：0.2.10 > 0.2.9
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// 下载到临时文件，大小和 SHA-256 都对上才返回
func downloadUpdate(f updFile) (string, error) {
	var last error
	for _, u := range f.URLs {
		path, err := downloadOne(u, f)
		if err == nil {
			return path, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("no download url")
	}
	return "", last
}

func downloadOne(u string, f updFile) (string, error) {
	c := &http.Client{Timeout: 10 * time.Minute}
	r, err := c.Get(u)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return "", fmt.Errorf("%s: %s", u, r.Status)
	}
	tmp, err := os.CreateTemp("", "rdesk-update-*.zip")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r.Body, f.Size+1))
	tmp.Close()
	if err == nil && n != f.Size {
		err = fmt.Errorf("size %d, want %d", n, f.Size)
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != strings.ToLower(f.SHA256) {
		err = errors.New("sha256 mismatch")
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}
