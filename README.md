# RDesk Host — 用 iPhone 远程控制你的 Mac

RDesk 是一个类似 RustDesk 的远程桌面。这个仓库是 **Mac 被控端（RDesk Host）**，配合 iPhone App「RDesk 远控」使用。

- **Tailscale 点对点直连**：手机和 Mac 之间直接连，不经过任何中转服务器，也不用公网 IP、不用开端口
- **端到端加密**：画面、声音和键鼠操作全程 AES-256-GCM 加密，密钥由「设备 ID + 密码」推导
- **硬件 H.264 + 自适应码率**：Mac 硬件编码，最高 60 帧；网络变差自动降码率，宁可稍糊也不卡
- **系统声音**：远程看视频、听歌有声音
- **真实光标**：手机上显示 Mac 当前的光标样子（箭头、输入竖线、手形、拖拽…）
- **鼠标模式**：手指滑动移光标、点按就是点击，跟 RustDesk 一样不会点偏

[English](#english) · 介绍页：https://gphdigital.com/apps/rdesk

## 需要

- macOS 14 或更新（Apple 芯片和 Intel 都支持）
- [Tailscale](https://tailscale.com/download)：Mac 和 iPhone 都安装，并登录**同一个账号**（个人免费）
- iPhone 上的「RDesk 远控」App

## 安装和使用

1. 在 [Releases](https://github.com/oncet886/rdesk/releases/latest) 下载 `RDesk-Host-macOS-v*.zip`，解压后把 **RDesk Host** 拖进「应用程序」。
2. 第一次打开：**右键点 RDesk Host → 打开**（还没有 Apple 公证，直接双击会被系统拦下）。
3. 浏览器会自动打开控制面板，显示**远程地址、设备 ID、密码**。
4. 按提示在 系统设置 → 隐私与安全性 里打开「屏幕录制」和「辅助功能」（控制面板上有直达链接）。
5. 在 iPhone 上打开 Tailscale，再打开「RDesk 远控」，填入地址、ID、密码，点连接。

以后再双击 RDesk Host，会直接打开控制面板；可以在面板里换新密码或退出。

## 命令行

```bash
rdesk                       # 有 Tailscale 就直连；没有就等 Tailscale（装了 cloudflared 则走 Cloudflare 隧道）
rdesk host -relay URL       # 连到你自己部署的中继
rdesk relay -listen :8080   # 只跑中继服务器（VPS 上用）
rdesk -h                    # 全部参数
```

## 从源码构建

```bash
go build -o rdesk .            # 需要 Go 1.24+ 和 Xcode 命令行工具
zsh scripts/package-mac.sh     # 打包通用版 RDesk Host.app 到 dist/
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

**RDesk Host** is the macOS side of RDesk, a RustDesk-style remote desktop you drive from the **RDesk** iPhone app.

- Peer-to-peer over **Tailscale**: no relay servers, no public IP, no port forwarding
- **End-to-end encrypted** (AES-256-GCM, key derived from device ID + password)
- **Hardware H.264** up to 60 fps with **adaptive bitrate**, plus **system audio**
- Shows the Mac's **real cursor shape**; **mouse mode** on the phone like RustDesk

**Requirements:** macOS 14+, Tailscale on both the Mac and the iPhone (same account), the RDesk iPhone app.

**Install:** download `RDesk-Host-macOS-v*.zip` from [Releases](https://github.com/oncet886/rdesk/releases/latest), move **RDesk Host** to Applications, **right-click → Open** the first time (not notarized yet). A control panel opens in your browser with the address, device ID and password. Grant Screen Recording and Accessibility, then connect from the iPhone app.

License: MIT.
