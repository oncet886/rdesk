#!/usr/bin/env zsh
# 打包 Windows 被控端：dist/RDesk-Host-Windows-x64-v<版本>.zip、dist/RDesk-Host-Windows-arm64-v<版本>.zip
# 在 Mac 上用 zig 交叉编译（cgo：DXGI 截屏 + Media Foundation H.264 + WASAPI 声音）。需要 brew install zig。
# 用法: zsh scripts/package-windows.sh（在 package-mac.sh 之后运行，它会清空 dist/）
set -euo pipefail
cd "$(dirname "$0")/.."
VER=$(grep -E '^const version' main.go | grep -oE '[0-9]+\.[0-9]+\.[0-9]+')
mkdir -p dist
for pair in amd64:x86_64:x64 arm64:aarch64:arm64; do
  GA=${pair%%:*}; rest=${pair#*:}; ZT=${rest%%:*}; NAME=${rest#*:}
  D="dist/RDesk-Host-Windows-$NAME-v$VER"
  rm -rf "$D" && mkdir -p "$D"
  CGO_ENABLED=1 GOOS=windows GOARCH=$GA CC="zig cc -target $ZT-windows-gnu" \
    go build -trimpath -ldflags "-s -w" -o "$D/rdesk.exe" .
  cat > "$D/README.txt" <<EOF
RDesk Host for Windows v$VER（测试版 / Beta）

【中文】
1. 先安装 Tailscale（https://tailscale.com/download/windows），登录和手机同一个账号。
2. 双击 rdesk.exe。
   · 第一次会提示「Windows 已保护你的电脑」：点「更多信息」→「仍要运行」（测试版还没有数字签名）。
   · 防火墙询问时，勾选「专用网络」和「公用网络」，点「允许」。
3. 窗口里会显示远程地址、设备 ID 和密码，填到手机或电脑上的 RDesk 远控里即可连接。
   这个窗口不要关，关掉就停止被控。
限制：Windows 锁屏和 UAC 弹窗（安全桌面）期间看不到、也控制不了，需要在本机解锁。
更多：https://rdeskapp.com

[English]
1. Install Tailscale (https://tailscale.com/download/windows) and sign in to the same account as your phone.
2. Double-click rdesk.exe.
   - If Windows SmartScreen says "Windows protected your PC", click "More info" → "Run anyway" (the beta is not code-signed yet).
   - When the firewall asks, allow both Private and Public networks.
3. The window shows the address, device ID and password. Enter them in the RDesk app. Keep the window open.
Limitation: the lock screen and UAC prompts (secure desktop) cannot be seen or controlled remotely yet.
More: https://rdeskapp.com
EOF
  (cd dist && rm -f "$(basename "$D").zip" && zip -qr "$(basename "$D").zip" "$(basename "$D")")
  rm -rf "$D"
done
ls -la dist/RDesk-Host-Windows-*.zip
