#!/usr/bin/env python3
"""生成 rdeskapp.com 整站到 dist/。样式沿用 gphdigital.com（assets/styles.css）。用法：python3 build.py"""
import html, pathlib, shutil, time

VERSION = time.strftime("%Y%m%d%H%M")
SITE = "https://rdeskapp.com"
GH_LATEST = "https://github.com/oncet886/rdesk/releases/latest"
GH = "https://github.com/oncet886/rdesk"
MAIL = "admin@gphautoads.com"
FONTS = '<link rel="preconnect" href="https://fonts.googleapis.com"><link rel="preconnect" href="https://fonts.gstatic.com" crossorigin><link href="https://fonts.googleapis.com/css2?family=Newsreader:ital,opsz,wght@0,6..72,400..600;1,6..72,400..600&family=Public+Sans:wght@400;600;700;800&display=swap" rel="stylesheet">'
SUN = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>'
DL = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M12 4v11M7 10l5 5 5-5"/><path d="M5 20h14"/></svg>'

# 各平台状态：上架后把 None 换成商店链接即可
APP_STORE = None   # "https://apps.apple.com/app/id6826686347"
GOOGLE_PLAY = None  # "https://play.google.com/store/apps/details?id=com.gphdigital.rdesk"

T = {
    "zh": dict(lang="zh-Hans", home="/", nav=[("/#features", "功能"), ("/#download", "下载"), ("/#start", "使用方法"), ("/privacy/", "隐私")],
               other=("/en/", "English"), skip="跳到正文", theme="切换外观", menu="打开导航",
               foot="用手机或电脑远程控制你自己的电脑。Tailscale 直连、端到端加密。",
               legal="隐私政策", contact="联系我们", source="源代码", company="© 2026 GPH Digital LLC · 美国亚利桑那州"),
    "en": dict(lang="en", home="/en/", nav=[("/en/#features", "Features"), ("/en/#download", "Download"), ("/en/#start", "Get started"), ("/privacy/#en", "Privacy")],
               other=("/", "中文"), skip="Skip to main content", theme="Change color theme", menu="Open navigation",
               foot="Control your own computer from your phone or another computer. Direct over Tailscale, end-to-end encrypted.",
               legal="Privacy Policy", contact="Contact", source="Source code", company="© 2026 GPH Digital LLC · Arizona, USA"),
}


def shell(lang, path, title, desc, body):
    t = T[lang]
    url = SITE + path
    nav = "".join(f'<a href="{h}">{x}</a>' for h, x in t["nav"])
    nav += f'<a href="{t["other"][0]}" hreflang="{"en" if lang == "zh" else "zh-Hans"}">{t["other"][1]}</a>'
    alt = ""
    if path in ("/", "/en/"):
        alt = (f'<link rel="alternate" hreflang="zh-Hans" href="{SITE}/"><link rel="alternate" hreflang="en" href="{SITE}/en/">'
               f'<link rel="alternate" hreflang="x-default" href="{SITE}/">')
    return f"""<!doctype html>
<html lang="{t["lang"]}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta name="theme-color" content="#f7f4ee">
<meta name="description" content="{html.escape(desc, quote=True)}">
<meta property="og:type" content="website">
<meta property="og:site_name" content="RDesk">
<meta property="og:title" content="{html.escape(title, quote=True)}">
<meta property="og:description" content="{html.escape(desc, quote=True)}">
<meta property="og:image" content="{SITE}/assets/icon-180.png">
<meta property="og:url" content="{url}">
<link rel="canonical" href="{url}">{alt}
<link rel="icon" type="image/png" href="/assets/favicon.png">
<link rel="apple-touch-icon" href="/assets/icon-180.png">
{FONTS}
<link rel="stylesheet" href="/assets/styles.css?v={VERSION}">
<style>
.brand img{{width:30px;height:30px;border-radius:8px;margin-right:10px;vertical-align:middle}}
.platforms{{display:grid;grid-template-columns:repeat(auto-fit,minmax(230px,1fr));gap:16px;margin-top:28px}}
.platform{{border:1px solid var(--line,rgba(0,0,0,.12));border-radius:18px;padding:20px 20px 18px;display:flex;flex-direction:column;gap:8px}}
.platform h3{{margin:0;font-size:19px}}
.platform p{{margin:0;font-size:15px;opacity:.75}}
.platform .button{{margin-top:auto;align-self:flex-start}}
.platform .pill{{align-self:flex-start;font-size:13px;font-weight:700;padding:4px 10px;border-radius:999px;background:rgba(127,127,127,.14)}}
.platform .pill.ok{{background:rgba(46,160,90,.16);color:#1f8a4c}}
#start ol li{{display:list-item}}
.role{{margin-top:34px;font-size:15px;font-weight:700;letter-spacing:.04em;opacity:.6}}
</style>
<script src="/assets/main.js?v={VERSION}" defer></script>
<title>{html.escape(title)}</title>
</head>
<body>
<a class="skip-link" href="#main">{t["skip"]}</a>
<header class="site-header"><div class="container header-inner">
  <a class="brand" href="{t["home"]}" aria-label="RDesk"><img src="/assets/icon-180.png" alt="" width="30" height="30">RDesk</a>
  <nav id="site-nav" class="site-nav" aria-label="Main navigation">{nav}</nav>
  <div class="header-actions">
    <button class="theme-toggle" type="button" aria-label="{t["theme"]}" title="{t["theme"]}">{SUN}</button>
    <button class="menu-toggle" type="button" aria-controls="site-nav" aria-expanded="false" aria-label="{t["menu"]}"><span></span><span></span></button>
  </div>
</div></header>
<main id="main">
{body}
</main>
<footer class="site-footer"><div class="container">
  <div class="footer-top">
    <div><a class="footer-brand" href="{t["home"]}">RDesk<span class="footer-period">.</span></a><p>{t["foot"]}</p></div>
    <div class="footer-links"><p class="footer-label">RDesk</p><a href="{t["nav"][1][0]}">{t["nav"][1][1]}</a><a href="{GH}" rel="noopener">{t["source"]}</a><a href="mailto:{MAIL}?subject=RDesk">{t["contact"]}</a></div>
    <div class="footer-links"><p class="footer-label">Legal</p><a href="{t["nav"][3][0]}">{t["legal"]}</a><a href="https://gphdigital.com/terms" rel="noopener">Terms of Use</a><a href="https://gphdigital.com/" rel="noopener">GPH Digital LLC</a></div>
  </div>
  <div class="footer-bottom"><span>{t["company"]}</span><span>rdeskapp.com</span></div>
</div></footer>
</body>
</html>
"""


def platform(name, desc, status, href=None, button=None, ok=False):
    pill = f'<span class="pill{" ok" if ok else ""}">{status}</span>'
    btn = f'<a class="button button-primary" href="{href}" rel="noopener">{DL}{button}</a>' if href else ""
    return f'<div class="platform">{pill}<h3>{name}</h3><p>{desc}</p>{btn}</div>'


def downloads(lang):
    zh = lang == "zh"
    ios = (platform("iPhone / iPad", "iOS 17 及以上" if zh else "iOS 17 or later", "App Store" if APP_STORE else ("审核中" if zh else "In review"),
                    APP_STORE, "App Store", ok=bool(APP_STORE)))
    android = (platform("Android", "Android 8 及以上" if zh else "Android 8 or later", "Google Play" if GOOGLE_PLAY else ("审核中" if zh else "In review"),
                        GOOGLE_PLAY, "Google Play", ok=bool(GOOGLE_PLAY)))
    soon = "开发中" if zh else "Coming soon"
    if zh:
        hosts = (platform("Mac 被控端 · RDesk Host", "macOS 14 及以上，Apple 芯片和 Intel 通用。开源免费，已通过 Apple 公证。", "可下载", GH_LATEST, "免费下载", ok=True)
                 + platform("Windows 被控端", "Windows 10 / 11（x64 和 ARM64）。测试版，暂未数字签名，第一次运行需在提示里点「仍要运行」。", "测试版", GH_LATEST, "免费下载", ok=True))
        ctrls = ios + android + platform("Mac 控制端", "macOS 14 及以上。在 Mac 上控制另一台电脑，键盘鼠标直接转发，连 Windows 时 ⌘ 自动当 Ctrl。", "测试版", GH_LATEST, "免费下载", ok=True) + platform("Windows 控制端", "Windows 10 / 11（x64 和 ARM64）。双击打开独立窗口，可控制 Mac 和 Windows，连 Mac 时 Ctrl 自动当 ⌘。测试版，暂未数字签名。", "测试版", GH_LATEST, "免费下载", ok=True)
        return f"""<section class="app-section" id="download"><div class="container">
  <p class="eyebrow">下载</p><h2>被控端装在要被控制的电脑上，<em>控制端装在你手上的设备。</em></h2>
  <p class="role">被控端 · 要被控制的电脑</p><div class="platforms">{hosts}</div>
  <p class="role">控制端 · 用来操作的设备</p><div class="platforms">{ctrls}</div>
</div></section>"""
    hosts = (platform("Mac host · RDesk Host", "macOS 14 or later, Apple silicon and Intel. Free, open source and notarized by Apple.", "Available", GH_LATEST, "Free download", ok=True)
             + platform("Windows host", "Windows 10 / 11 (x64 and ARM64). Beta, not code-signed yet: choose \"Run anyway\" on first launch.", "Beta", GH_LATEST, "Free download", ok=True))
    ctrls = ios + android + platform("Mac viewer", "macOS 14 or later. Control another computer with your own keyboard and mouse; ⌘ maps to Ctrl on Windows.", "Beta", GH_LATEST, "Free download", ok=True) + platform("Windows viewer", "Windows 10 / 11 (x64 and ARM64). Opens its own window and controls Macs and PCs; Ctrl acts as ⌘ on a Mac. Beta, not code-signed yet.", "Beta", GH_LATEST, "Free download", ok=True)
    return f"""<section class="app-section" id="download"><div class="container">
  <p class="eyebrow">Download</p><h2>Install the host on the computer you control, <em>the app on the device in your hand.</em></h2>
  <p class="role">HOST · THE COMPUTER BEING CONTROLLED</p><div class="platforms">{hosts}</div>
  <p class="role">VIEWER · THE DEVICE YOU CONTROL IT FROM</p><div class="platforms">{ctrls}</div>
</div></section>"""


ZH = f"""<section class="app-hero"><div class="container app-hero-grid">
  <div><p class="eyebrow">远程桌面 · Tailscale 直连</p><h1>RDesk 远控</h1><p class="app-tagline">在手机上控制你自己的电脑</p>
  <p class="lead" style="margin-top:20px">手机和电脑通过 Tailscale 点对点直连，不经过任何中转服务器，画面、声音和每一次点按全程端到端加密。硬件视频最高每秒 60 帧，听得到电脑的声音，看得到真实的光标。</p>
  <div class="hero-actions download-actions"><a class="button button-primary" href="#download">{DL}下载</a><a class="button button-ghost" href="{GH}" rel="noopener">GitHub 源代码</a></div>
  <p style="margin-top:22px"><span class="status-pill">免费 · 被控端开源（MIT）</span></p></div>
  <div class="app-hero-media phone"><div class="device-phone"><img src="/assets/rdesk-iphone-start.jpg" width="900" height="1957" alt="iPhone 上的 RDesk 远控：Tailscale 已连接、三步开始，以及地址、设备 ID、密码输入框" fetchpriority="high"></div></div>
</div></section>
<section class="section-tight hairline"><div class="container"><dl class="facts app-facts">
  <div><dt>控制端</dt><dd>iPhone / iPad、安卓、Mac、Windows</dd></div>
  <div><dt>被控端</dt><dd>Mac（macOS 14+）、Windows 10 / 11</dd></div>
  <div><dt>连接</dt><dd>Tailscale 点对点直连，不经过中转</dd></div>
  <div><dt>价格</dt><dd>免费，没有账号、没有广告</dd></div>
</dl></div></section>
<section class="app-section" id="features"><div class="container">
  <div class="feature-grid">
    <div class="feature"><h3>直连，不中转</h3><p>手机和电脑通过 Tailscale 点对点直接相连，不需要公网 IP，也不用在路由器上开端口。Wi-Fi 和移动网络都一样能用。</p></div>
    <div class="feature"><h3>端到端加密</h3><p>画面、声音和每一次点按都用 AES-256-GCM 加密，密钥由设备 ID 和密码推导。我们没有服务器，也看不到你的屏幕。</p></div>
    <div class="feature"><h3>手机网络也流畅</h3><p>电脑用硬件 H.264 编码，最高每秒 60 帧。网络一忙码率立刻降下来，宁可稍糊也不卡，恢复后自动升回去。</p></div>
    <div class="feature"><h3>听得到电脑的声音</h3><p>在电脑上看视频、听歌，手机上同步有声音。一键静音，静音后不再传声音，省流量。</p></div>
    <div class="feature"><h3>真实光标 + 鼠标模式</h3><p>显示电脑当前真实的光标：箭头、输入竖线、手形。手指滑动移光标、轻点就是点击，小按钮也点得准；双指滚动、捏合放大。</p></div>
    <div class="feature"><h3>为手机设计</h3><p>一键横屏、支持中文输入法和外接键盘，特殊键面板有 Esc、Tab、方向键、复制粘贴和切换应用，白天 / 夜间外观。</p></div>
  </div>
</div></section>
{downloads("zh")}
<section class="app-section alt" id="start"><div class="container split">
  <div class="phone-row"><div class="device-phone"><img src="/assets/rdesk-iphone-light.jpg" width="900" height="1957" alt="RDesk 远控白天模式的开始页面，带三步上手说明" loading="lazy"></div><div class="device-phone"><img src="/assets/rdesk-iphone-tailscale.jpg" width="900" height="1957" alt="RDesk 远控提示没检测到 Tailscale，并提供下载按钮" loading="lazy"></div></div>
  <div><h2>三步 <em>开始使用。</em></h2>
    <ol class="lead" style="margin-top:18px;padding-left:1.2em;line-height:1.8">
      <li>在要被控制的电脑上下载 <a class="textlink" href="{GH_LATEST}" rel="noopener">RDesk Host</a>。Mac：拖进「应用程序」并打开，允许「屏幕录制」和「辅助功能」，菜单栏图标里能看到地址、设备 ID 和密码；Windows：解压后双击 rdesk.exe，窗口里会显示这三项。</li>
      <li>电脑和手机都安装 <a class="textlink" href="https://tailscale.com/download" rel="noopener">Tailscale</a>，登录同一个账号（个人使用免费）。</li>
      <li>打开手机上的 RDesk 远控，填入地址、设备 ID、密码，点「连接」。</li>
    </ol>
    <p class="muted" style="margin-top:14px;font-size:15px">App 会自动检测 Tailscale，没连上会提示你怎么做。</p>
  </div>
</div></section>
<section class="section-tight"><div class="container"><div class="band"><div><p class="eyebrow">有问题？</p><h2>需要帮助或想要 <em>新功能？</em></h2></div><a class="button button-primary" href="mailto:{MAIL}?subject=RDesk">发邮件给我们</a></div></div></section>"""

EN = f"""<section class="app-hero"><div class="container app-hero-grid">
  <div><p class="eyebrow">Remote desktop · Direct over Tailscale</p><h1>RDesk</h1><p class="app-tagline">Control your own computer from your phone</p>
  <p class="lead" style="margin-top:20px">Your phone connects to your computer peer to peer over Tailscale, with no relay server in between. Video, sound and every tap are end-to-end encrypted. Hardware video up to 60 fps, your computer's audio, and its real cursor.</p>
  <div class="hero-actions download-actions"><a class="button button-primary" href="#download">{DL}Download</a><a class="button button-ghost" href="{GH}" rel="noopener">Source on GitHub</a></div>
  <p style="margin-top:22px"><span class="status-pill">Free · Open-source host (MIT)</span></p></div>
  <div class="app-hero-media phone"><div class="device-phone"><img src="/assets/rdesk-iphone-start.jpg" width="900" height="1957" alt="RDesk on iPhone: Tailscale connected, three setup steps, and fields for address, device ID and password" fetchpriority="high"></div></div>
</div></section>
<section class="section-tight hairline"><div class="container"><dl class="facts app-facts">
  <div><dt>Viewer</dt><dd>iPhone / iPad, Android, Mac, Windows</dd></div>
  <div><dt>Host</dt><dd>Mac (macOS 14+), Windows 10 / 11</dd></div>
  <div><dt>Connection</dt><dd>Peer to peer over Tailscale, no relay</dd></div>
  <div><dt>Price</dt><dd>Free. No accounts, no ads</dd></div>
</dl></div></section>
<section class="app-section" id="features"><div class="container">
  <div class="feature-grid">
    <div class="feature"><h3>Direct, not relayed</h3><p>Your phone and computer connect directly over Tailscale. No public IP, no port forwarding. Works on Wi-Fi and cellular.</p></div>
    <div class="feature"><h3>End-to-end encrypted</h3><p>Video, sound and every tap are encrypted with AES-256-GCM, keyed from the device ID and password. There is no RDesk server and we never see your screen.</p></div>
    <div class="feature"><h3>Smooth on cellular</h3><p>Hardware H.264 up to 60 fps. When the network gets busy the bitrate drops at once, so the picture softens instead of freezing, then climbs back.</p></div>
    <div class="feature"><h3>Hear your computer</h3><p>Videos and music on your computer play on your phone too. One tap mutes it and stops sending audio to save data.</p></div>
    <div class="feature"><h3>Real cursor, mouse mode</h3><p>See the computer's actual cursor. Slide anywhere to move it, tap to click, even on tiny buttons. Two fingers scroll, pinch to zoom.</p></div>
    <div class="feature"><h3>Made for phones</h3><p>One-tap landscape, Chinese input and hardware keyboards, a special-keys panel with Esc, Tab, arrows, copy and paste, and light and dark themes.</p></div>
  </div>
</div></section>
{downloads("en")}
<section class="app-section alt" id="start"><div class="container split">
  <div class="phone-row"><div class="device-phone"><img src="/assets/rdesk-iphone-light.jpg" width="900" height="1957" alt="RDesk start screen in light mode with three setup steps" loading="lazy"></div><div class="device-phone"><img src="/assets/rdesk-iphone-tailscale.jpg" width="900" height="1957" alt="RDesk telling you Tailscale is not connected, with a button to get it" loading="lazy"></div></div>
  <div><h2>Three steps <em>to start.</em></h2>
    <ol class="lead" style="margin-top:18px;padding-left:1.2em;line-height:1.8">
      <li>On the computer you want to control, download <a class="textlink" href="{GH_LATEST}" rel="noopener">RDesk Host</a>. Mac: move it to Applications, open it and allow Screen Recording and Accessibility; the menu bar icon shows the address, device ID and password. Windows: unzip and double-click rdesk.exe; its window shows the same three items.</li>
      <li>Install <a class="textlink" href="https://tailscale.com/download" rel="noopener">Tailscale</a> on both devices and sign in to the same account (free for personal use).</li>
      <li>Open RDesk on your phone, enter the address, device ID and password, and tap Connect. The interface is in Simplified Chinese.</li>
    </ol>
  </div>
</div></section>
<section class="section-tight"><div class="container"><div class="band"><div><p class="eyebrow">Questions?</p><h2>Need help or want <em>a feature?</em></h2></div><a class="button button-primary" href="mailto:{MAIL}?subject=RDesk">Email us</a></div></div></section>"""

PRIVACY = f"""<section class="app-section"><div class="container" style="max-width:760px">
<p class="eyebrow">隐私政策 · Privacy Policy</p><h1>RDesk 隐私政策</h1><p class="muted">生效日期：2026 年 10 月 24 日 · GPH Digital LLC</p>
<div class="lead" style="margin-top:22px;line-height:1.85">
<p>RDesk（包括手机和电脑上的控制端，以及被控端 RDesk Host）不收集任何个人数据：没有账号、没有统计分析、没有广告，也没有第三方 SDK。</p>
<p>你连接过的电脑列表和你选择记住的密码只保存在你自己的设备上，密码用系统钥匙串 / Keystore 加密。画面、声音以及你的点按和键盘输入只在你自己的设备之间直接传输，全程用 AES-256-GCM 端到端加密，通常经过你自己的 Tailscale 网络。GPH Digital LLC 在这条路径上没有任何服务器，看不到其中任何内容。App 只申请网络权限。</p>
<p>RDesk Host 运行在你自己的电脑上，不会向我们发送任何数据，源代码公开在 <a class="textlink" href="{GH}" rel="noopener">github.com/oncet886/rdesk</a>；从 GitHub 下载适用 GitHub 的隐私声明。Tailscale 是你自行安装和登录的独立服务，适用 Tailscale 自己的条款。</p>
<p>如有问题请联系 <a class="textlink" href="mailto:{MAIL}">{MAIL}</a>。</p>
</div>
<h2 id="en" style="margin-top:48px">Privacy Policy (English)</h2><p class="muted">Effective October 24, 2026 · GPH Digital LLC</p>
<div class="lead" style="margin-top:18px;line-height:1.75">
<p>RDesk (the viewer apps for phones and computers, and the RDesk Host companion app) does not collect any personal data. It has no accounts, analytics, advertising or third-party SDKs.</p>
<p>The list of computers you connect to and any password you choose to remember are stored only on your own device; remembered passwords are encrypted with the system keychain or keystore. Screen images, sound and your taps and keystrokes travel directly between your own devices, encrypted end to end with AES-256-GCM, usually over your own Tailscale network. GPH Digital LLC operates no server in that path and cannot see any of it. The apps ask only for network access.</p>
<p>RDesk Host runs on your own computer, sends nothing to us, and is open source at <a class="textlink" href="{GH}" rel="noopener">github.com/oncet886/rdesk</a>; downloading it from GitHub is subject to GitHub's privacy statement. Tailscale is a separate service you install and sign in to under Tailscale's own terms.</p>
<p>Questions: <a class="textlink" href="mailto:{MAIL}">{MAIL}</a>.</p>
</div></div></section>"""

PAGES = [
    ("zh", "/", "index.html", "RDesk 远控 · 用手机远程控制你的电脑",
     "RDesk 远控：用手机远程控制你自己的电脑。Tailscale 直连、端到端加密，硬件 H.264 最高 60 帧，带声音和真实光标。被控端开源免费。", ZH),
    ("en", "/en/", "en/index.html", "RDesk · Control your computer from your phone",
     "RDesk: control your own computer from your phone over a direct, end-to-end encrypted Tailscale connection. Hardware H.264 up to 60 fps, audio and the real cursor. Free, open-source host.", EN),
    ("zh", "/privacy/", "privacy/index.html", "隐私政策 · RDesk",
     "RDesk 不收集任何个人数据。RDesk collects no personal data.", PRIVACY),
]

if __name__ == "__main__":
    root = pathlib.Path(__file__).parent
    out = root / "dist"
    shutil.rmtree(out, ignore_errors=True)
    shutil.copytree(root / "assets", out / "assets")
    for lang, path, file, title, desc, body in PAGES:
        p = out / file
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(shell(lang, path, title, desc, body), encoding="utf-8")
    (out / "robots.txt").write_text(f"User-agent: *\nAllow: /\nSitemap: {SITE}/sitemap.xml\n")
    urls = "".join(f"<url><loc>{SITE}{p}</loc></url>" for _, p, *_ in PAGES)
    (out / "sitemap.xml").write_text(f'<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">{urls}</urlset>\n')
    print("ok:", ", ".join(f for _, _, f, *_ in PAGES))
