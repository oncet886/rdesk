# RDesk Host — 用手机远程控制你的电脑

RDesk 是一个类似 RustDesk 的远程桌面。这个仓库是 **被控端（RDesk Host）**，支持 **Mac** 和 **Windows**，配合「RDesk 远控」控制端使用（iPhone / iPad、安卓、Mac）。

- **Tailscale 点对点直连**：手机和 Mac 之间直接连，不经过任何中转服务器，也不用公网 IP、不用开端口
- **端到端加密**：画面、声音和键鼠操作全程 AES-256-GCM 加密，密钥由「设备 ID + 密码」推导
- **H.264 + 自适应码率**：Mac 硬件编码 / Windows 用系统自带编码器，最高 60 帧；网络变差自动降码率，宁可稍糊也不卡
- **系统声音**：远程看视频、听歌有声音
- **真实光标**：控制端显示被控电脑当前的光标样子（箭头、输入竖线、手形、拖拽…）
- **鼠标模式**：手指滑动移光标、点按就是点击，跟 RustDesk 一样不会点偏

[English](#english) · 官网：https://rdeskapp.com

## 需要

- 被控端：macOS 14 或更新（Apple 芯片和 Intel 都支持），或 Windows 10 / 11（x64、ARM64）
- [Tailscale](https://tailscale.com/download)：被控电脑和控制端都安装，并登录**同一个账号**（个人免费）
- 控制端「RDesk 远控」：iPhone / iPad、安卓，或 Mac 控制端（[Releases](https://github.com/oncet886/rdesk/releases/latest) 里的 `RDesk-Mac-Viewer-*.zip`）

## 安装和使用（Mac）

1. 在 [Releases](https://github.com/oncet886/rdesk/releases/latest) 下载 `RDesk-Host-macOS-v*.zip`，解压后把 **RDesk Host** 拖进「应用程序」。
2. 双击打开（已通过 Apple 公证）。
3. 第一次会自动打开控制面板，显示**远程地址、设备 ID、密码**。之后点**菜单栏的 RDesk 图标**（显示器加箭头）就能看到地址、ID、密码（点一下就复制）、连接状态、Tailscale 和权限状态，还能换新密码、设置开机自启、退出。
4. 按提示在 系统设置 → 隐私与安全性 里打开「屏幕录制」和「辅助功能」（控制面板上有直达链接）。
5. 在 iPhone 上打开 Tailscale，再打开「RDesk 远控」，填入地址、ID、密码，点连接。

手机连上时菜单栏图标会变成实心。菜单栏图标多、被刘海挡住时，按住 ⌘ 把它拖到右边即可。

## 安装和使用（Windows，测试版）

1. 在 [Releases](https://github.com/oncet886/rdesk/releases/latest) 下载 `RDesk-Host-Windows-x64-*.zip`（ARM 电脑选 `arm64`），解压。
2. 双击 `rdesk.exe`。测试版还没有数字签名：出现「Windows 已保护你的电脑」时点「更多信息」→「仍要运行」；防火墙询问时允许「专用网络」和「公用网络」。
3. 窗口里显示**远程地址、设备 ID、密码**，填到控制端即可。窗口不要关。

已知限制：Windows 锁屏和 UAC 弹窗（安全桌面）期间无法远程查看和操作。

## 命令行

```bash
rdesk                       # 有 Tailscale 就直连；没有就等 Tailscale（装了 cloudflared 则走 Cloudflare 隧道）
rdesk host -relay URL       # 连到你自己部署的中继
rdesk relay -listen :8080   # 只跑中继服务器（VPS 上用）
rdesk -h                    # 全部参数
```

## 从源码构建

```bash
go build -o rdesk .              # 需要 Go 1.24+ 和 Xcode 命令行工具
zsh scripts/package-mac.sh       # 打包通用版 RDesk Host.app 到 dist/（有签名环境时自动签名+公证）
zsh scripts/package-windows.sh   # 在 Mac 上交叉编译 Windows x64 / ARM64 版（需要 brew install zig）
```

## 安全说明

- 控制端必须知道 **设备 ID + 密码** 才能解密画面和发送操作；密码连错 5 次锁定 5 分钟。
- 直连模式只监听 Tailscale 网卡，只有你 tailnet 里的设备能连上。
- 控制面板只监听 `127.0.0.1`，并校验来源，其他网站读不到你的密码。
- 不会占用 RustDesk 的端口（21115–21119），可以和 RustDesk 同时运行。

## 许可

MIT © GPH Digital LLC

---

## English

**RDesk Host** is the host side of RDesk, a RustDesk-style remote desktop, for **macOS** and **Windows**. Control it from the **RDesk** app on iPhone / iPad, Android or Mac. Website: https://rdeskapp.com

- Peer-to-peer over **Tailscale**: no relay servers, no public IP, no port forwarding
- **End-to-end encrypted** (AES-256-GCM, key derived from device ID + password)
- **H.264** up to 60 fps (hardware on Mac, Media Foundation on Windows) with **adaptive bitrate**, plus **system audio**
- Shows the Mac's **real cursor shape**; **mouse mode** on the phone like RustDesk
- **Menu bar icon** with the address, device ID and password (click to copy), connection, Tailscale and permission status

**Requirements:** macOS 14+ or Windows 10 / 11 (x64, ARM64); Tailscale on both devices (same account); an RDesk viewer (iPhone / iPad, Android, or the Mac viewer from Releases).

**Install:** download `RDesk-Host-macOS-v*.zip` from [Releases](https://github.com/oncet886/rdesk/releases/latest), move **RDesk Host** to Applications and open it (signed and notarized by Apple). A control panel opens in your browser with the address, device ID and password. Grant Screen Recording and Accessibility, then connect from the app.

**Windows (beta):** download `RDesk-Host-Windows-x64-*.zip` (or `arm64`), unzip and run `rdesk.exe`. It is not code-signed yet: choose "More info" → "Run anyway", and allow it through the firewall. The lock screen and UAC prompts cannot be viewed or controlled remotely yet.

License: MIT.
