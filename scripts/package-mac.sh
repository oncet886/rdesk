#!/usr/bin/env zsh
# 打包 macOS 被控端：dist/RDesk-Host-macOS-v<版本>.zip（内含 RDesk Host.app，Apple 芯片 + Intel 通用）
# 有签名环境时：Developer ID 签名 + 苹果公证 + 装订，用户下载后直接双击打开；没有则退回临时签名。
# 用法: zsh scripts/package-mac.sh
#
# 签名环境（只在维护者的 Mac 上）：
#   钥匙串 ~/Library/Keychains/rdesk-signing.keychain-db，解锁密码在 ~/.config/rdesk/signing-keychain-pass
#   公证凭据存在同一钥匙串的 profile "rdesk-notary"（App Store Connect API 密钥）
set -euo pipefail
cd "$(dirname "$0")/.."
VER=$(grep -E '^const version' main.go | grep -oE '[0-9]+\.[0-9]+\.[0-9]+')
APP="dist/RDesk Host.app"
KC="$HOME/Library/Keychains/rdesk-signing.keychain-db"
PASSF="$HOME/.config/rdesk/signing-keychain-pass"
IDENTITY="Developer ID Application: GPH Digital LLC (PPHK7JL7YT)"

rm -rf dist && mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
export CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=14.0
GOARCH=arm64 CC="clang -arch arm64"  go build -trimpath -ldflags "-s -w" -o dist/rdesk-arm64 .
GOARCH=amd64 CC="clang -arch x86_64" go build -trimpath -ldflags "-s -w" -o dist/rdesk-amd64 .
lipo -create -output "$APP/Contents/MacOS/rdesk" dist/rdesk-arm64 dist/rdesk-amd64
cp "$APP/Contents/MacOS/rdesk" dist/rdesk   # 命令行版（同一个二进制）
rm dist/rdesk-arm64 dist/rdesk-amd64
sed "s/__VERSION__/$VER/g" packaging/Info.plist > "$APP/Contents/Info.plist"
ICON=packaging/icon-1024.png
SET=dist/AppIcon.iconset && mkdir -p $SET
for s in 16 32 128 256 512; do
  sips -z $s $s $ICON --out $SET/icon_${s}x${s}.png >/dev/null
  sips -z $((s*2)) $((s*2)) $ICON --out $SET/icon_${s}x${s}@2x.png >/dev/null
done
iconutil -c icns $SET -o "$APP/Contents/Resources/AppIcon.icns" && rm -rf $SET

if [[ -f "$KC" && -f "$PASSF" ]]; then
  security unlock-keychain -p "$(cat "$PASSF")" "$KC"
  SIGN=(codesign --force --options runtime --timestamp --keychain "$KC" -s "$IDENTITY")
  "${SIGN[@]}" "$APP/Contents/MacOS/rdesk"
  "${SIGN[@]}" "$APP"
  "${SIGN[@]}" dist/rdesk
  echo "→ 提交苹果公证（通常 1~5 分钟）…"
  (cd dist && ditto -c -k --keepParent "RDesk Host.app" notarize-app.zip && zip -q notarize-cli.zip rdesk)
  NT=(xcrun notarytool submit --keychain-profile rdesk-notary --keychain "$KC" --wait)
  "${NT[@]}" dist/notarize-app.zip | tail -2
  "${NT[@]}" dist/notarize-cli.zip | tail -2
  xcrun stapler staple "$APP"
  rm dist/notarize-app.zip dist/notarize-cli.zip
  spctl -a -vv "$APP" 2>&1 | head -3
else
  echo "⚠️ 没有签名环境，使用临时签名（用户首次需右键→打开）"
  codesign --force --deep -s - "$APP"
  codesign --force -s - dist/rdesk
fi
(cd dist && ditto -c -k --keepParent "RDesk Host.app" "RDesk-Host-macOS-v$VER.zip" && zip -q "rdesk-cli-macOS-v$VER.zip" rdesk)
lipo -info "$APP/Contents/MacOS/rdesk"
ls -la dist/*.zip
