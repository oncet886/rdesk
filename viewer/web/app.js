"use strict";
// RDesk 网页控制端：协议和 iPhone / 安卓 / Mac 控制端一致（见被控端 host.go）。
// 加解密（PBKDF2 + AES-256-GCM）、H.264 / AAC 解码都在浏览器里完成；
// 由电脑控制端打开时（地址带 ?t=令牌）经 127.0.0.1 的本地转发连接被控端。

const $ = (s) => document.querySelector(s);
const enc = new TextEncoder(), dec = new TextDecoder();
const TOKEN = new URLSearchParams(location.search).get("t");
const CLIENT_MAC = /Mac/.test(navigator.platform);

const M = { HELLO: 0x01, ACK: 0x02, PING: 0x03, KEYREQ: 0x04, MOVE: 0x10, BUTTON: 0x11, WHEEL: 0x12, KEY: 0x20, TEXT: 0x21,
  SETTINGS: 0x30, STATS: 0x31, HELLO_OK: 0x81, FRAME: 0x82, PONG: 0x83, INFO: 0x84, VIDEO: 0x85, CURSOR: 0x86, AUDIO: 0x87, CURSOR_SHAPE: 0x88 };
const AAD_V2H = enc.encode("v2h"), AAD_H2V = enc.encode("h2v");
const QUALITY = { low: { q: 45, maxw: 1280, fps: 30 }, mid: { q: 65, maxw: 1920, fps: 30 }, high: { q: 85, maxw: 2560, fps: 60 } };

// ---------------- 界面文字 ----------------
document.querySelectorAll("[data-t]").forEach((e) => (e.textContent = T[e.dataset.t] ?? e.textContent));
document.querySelectorAll("[data-ph]").forEach((e) => (e.placeholder = T[e.dataset.ph]));
document.title = T.title;

// 由电脑控制端打开时：定时报平安，窗口关掉后本地程序会自己退出
if (TOKEN) setInterval(() => fetch("/alive?t=" + TOKEN).catch(() => {}), 5000);

// ---------------- 记住的设备 ----------------
const store = {
  get(k, d) { try { return JSON.parse(localStorage.getItem("rdesk." + k)) ?? d; } catch { return d; } },
  set(k, v) { try { localStorage.setItem("rdesk." + k, JSON.stringify(v)); } catch {} },
};
const devKey = (d) => d.server + "|" + d.id;

function renderDevices() {
  const list = store.get("devices", []);
  $("#devices").hidden = !list.length;
  $("#start").hidden = !!list.length;
  $("#formTitle").textContent = list.length ? T.newConn : T.connTo;
  const box = $("#deviceList");
  box.textContent = "";
  for (const d of list) {
    const row = document.createElement("div");
    row.className = "card device";
    row.innerHTML = `<div class="ico">🖥</div><div class="meta"><b></b><span></span></div><button class="del" type="button"></button>`;
    row.querySelector("b").textContent = d.name || d.id;
    row.querySelector(".meta span").textContent = `ID ${d.id} · ${d.server}`;
    row.querySelector(".del").textContent = T.del;
    row.querySelector(".del").onclick = (e) => {
      e.stopPropagation();
      store.set("devices", store.get("devices", []).filter((x) => devKey(x) !== devKey(d)));
      const pws = store.get("pw", {}); delete pws[devKey(d)]; store.set("pw", pws);
      renderDevices();
    };
    row.onclick = () => {
      $("#server").value = d.server; $("#devid").value = d.id;
      $("#pw").value = store.get("pw", {})[devKey(d)] || "";
      if ($("#pw").value) start(); else $("#pw").focus();
    };
    box.appendChild(row);
  }
}
renderDevices();
const last = store.get("last", null);
if (last) { $("#server").value = last.server; $("#devid").value = last.id; $("#pw").value = store.get("pw", {})[devKey(last)] || ""; }
// 被控端给的链接：rdeskapp.com/web/#s=地址&id=设备ID（# 后面的内容不会发给网站；密码不放在链接里）
{
  const h = new URLSearchParams(location.hash.slice(1));
  // 网页由被控端自己提供（https://电脑名.ts.net:端口/）时，地址就是本页
  const self = !TOKEN && location.protocol === "https:" && !/(^|\.)rdeskapp\.com$/.test(location.hostname) ? location.host : "";
  if ((h.get("s") || self) && h.get("id")) {
    const d = { server: h.get("s") || self, id: h.get("id") };
    $("#server").value = d.server; $("#devid").value = d.id;
    $("#pw").value = store.get("pw", {})[devKey(d)] || "";
    setTimeout(() => $("#pw").value ? $("#go").focus() : $("#pw").focus(), 0);
  }
}

function showError(m) { const b = $("#error"); b.textContent = m; b.hidden = !m; }

// ---------------- 加密 ----------------
async function deriveKey(pw, id) {
  const base = await crypto.subtle.importKey("raw", enc.encode(pw), "PBKDF2", false, ["deriveKey"]);
  return crypto.subtle.deriveKey({ name: "PBKDF2", salt: enc.encode("rdesk:" + id), iterations: 150000, hash: "SHA-256" },
    base, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
}

// ---------------- 会话 ----------------
let S = null; // 当前会话

function wsURL(server, id) {
  if (TOKEN) return `ws://${location.host}/proxy?t=${TOKEN}&target=${encodeURIComponent(server)}&id=${encodeURIComponent(id)}`;
  // 官网版：https 页面只能连 wss（被控端要开 Tailscale HTTPS）
  let s = server.trim();
  const bare = !/^[a-z]+:\/\//i.test(s);
  if (bare) s = "wss://" + s;
  const u = new URL(s.replace(/^http/i, "ws"));
  // 只填了电脑名（如 xxx.ts.net）没写端口：被控端默认端口 21130；Cloudflare 临时分享地址走默认 443
  if (bare && !u.port && !/\.trycloudflare\.com$/i.test(u.hostname)) u.port = "21130";
  if (location.protocol === "https:" && u.protocol !== "wss:") return null;
  // Tailscale IP 没有证书，浏览器的加密连接一定失败：提示改用「浏览器连接」链接里的电脑名
  if (location.protocol === "https:" && /^100\.\d+\.\d+\.\d+$/.test(u.hostname)) return null;
  u.pathname = u.pathname.replace(/\/+$/, "").replace(/\/ws\/view$/, "") + "/ws/view";
  u.search = "?id=" + encodeURIComponent(id);
  return u.toString();
}

async function start() {
  const server = $("#server").value.trim(), id = $("#devid").value.replace(/\s/g, ""), pw = $("#pw").value;
  if (!server || !id || !pw) return;
  showError("");
  if (!window.isSecureContext || !crypto.subtle) return showError(T.errSecure);
  if (!("VideoDecoder" in window)) return showError(T.errCodec);
  let url;
  try { url = wsURL(server, id); } catch { return showError(T.errAddr); }
  if (!url) return showError(T.errSecure);
  setBusy(T.connecting);
  const key = await deriveKey(pw, id);
  S = new Session({ server, id, pw, aes: key, url, remember: $("#remember").checked });
}

function setBusy(label) {
  $("#go").disabled = !!label;
  $("#go").textContent = label || T.connect;
  $("#cancel").hidden = !label;
}

$("#form").onsubmit = (e) => { e.preventDefault(); start(); };
$("#cancel").onclick = () => S?.close();

class Session {
  constructor(o) {
    Object.assign(this, o);
    this.quality = store.get("quality", "mid");
    this.display = 0;
    this.audioOn = store.get("audioOn", true);
    this.frames = 0; this.bytes = 0; this.dropped = 0; this.rtt = 0; this.decodeMs = 0;
    this.pressed = new Set();
    this.ws = new WebSocket(o.url);
    this.ws.binaryType = "arraybuffer";
    this.ws.onmessage = (e) => this.onMessage(e.data);
    this.ws.onclose = () => this.closed();
    // 连不上时浏览器可能一直不报错（比如端口不对、对方不在 Tailscale 里）：12 秒还没连上就停
    this.openTimer = setTimeout(() => { if (!this.connected) { this.error = T.errConnect; this.close(); } }, 12000);
    this.ws.onerror = () => {};
    this.sendChain = Promise.resolve();
  }

  // 加密后按顺序发出（加密是异步的，用链保证顺序）
  send(bytes) {
    this.sendChain = this.sendChain.then(async () => {
      if (this.ws.readyState !== 1) return;
      const iv = crypto.getRandomValues(new Uint8Array(12));
      const ct = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv, additionalData: AAD_V2H }, this.aes, bytes));
      const out = new Uint8Array(13 + ct.length);
      out[0] = 1; out.set(iv, 1); out.set(ct, 13);
      this.ws.send(out);
    });
  }
  msg(type, ...parts) {
    let n = 1; for (const p of parts) n += p.length;
    const b = new Uint8Array(n); b[0] = type; let o = 1;
    for (const p of parts) { b.set(p, o); o += p.length; }
    this.send(b);
  }
  settingsJSON() {
    return enc.encode(JSON.stringify({ ...QUALITY[this.quality], display: this.display, h264: true, audio: this.audioOn ? 1 : 2 }));
  }

  async onMessage(data) {
    if (typeof data === "string") {
      let o; try { o = JSON.parse(data); } catch { return; }
      if (o.t === "paired") { setBusy(T.verifying); this.msg(M.HELLO, this.settingsJSON()); }
      else if (o.t === "err") this.fail(o.m || T.errConnect);
      return;
    }
    const d = new Uint8Array(data);
    if (d[0] === 0) return this.fail(dec.decode(d.subarray(1))); // 明文：密码错误 / 已锁定
    let p;
    try { p = new Uint8Array(await crypto.subtle.decrypt({ name: "AES-GCM", iv: d.subarray(1, 13), additionalData: AAD_H2V }, this.aes, d.subarray(13))); }
    catch { return; }
    this.bytes += d.length;
    const v = new DataView(p.buffer, p.byteOffset, p.byteLength);
    switch (p[0]) {
      case M.HELLO_OK: this.onHello(JSON.parse(dec.decode(p.subarray(1)))); break;
      case M.VIDEO: this.onVideo(p, v); break;
      case M.FRAME: this.onJPEG(p, v); break;
      case M.AUDIO: this.onAudio(p.subarray(1)); break;
      case M.CURSOR_SHAPE: this.onCursorShape(p, v); break;
      case M.CURSOR: this.onCursorPos(v.getUint16(1), v.getUint16(3)); break;
      case M.PONG: this.rtt = performance.now() - v.getFloat64(1); break;
      case M.INFO: toast(dec.decode(p.subarray(1))); break;
    }
  }

  onHello(info) {
    this.host = info;
    this.hostMac = info.os === "darwin";
    this.connected = true;
    clearTimeout(this.openTimer);
    this.cx = 32768; this.cy = 32768;
    setBusy("");
    // 记住设备（和密码）
    const dev = { server: this.server, id: this.id, name: info.name };
    store.set("devices", [dev, ...store.get("devices", []).filter((x) => devKey(x) !== devKey(dev))].slice(0, 20));
    store.set("last", { server: this.server, id: this.id });
    const pws = store.get("pw", {});
    if (this.remember) pws[devKey(dev)] = this.pw; else delete pws[devKey(dev)];
    store.set("pw", pws);
    renderDevices();
    showRemote(this);
    if (info.input === false) toast(T.noInput);
    else if (COARSE) toast(touchMode === "mouse" ? T.mouseHint : T.touchHint); // 触屏设备：提示手势
    this.startAudio();
    this.timer = setInterval(() => this.tick(), 1000);
  }

  // ----- 画面：WebCodecs 硬件解码 H.264（Annex-B）-----
  onVideo(p, v) {
    const seq = v.getUint32(1), w = v.getUint16(5), h = v.getUint16(7), key = p[9] === 1, data = p.subarray(10);
    if (!this.decoder || this.decoder.state === "closed") this.makeDecoder();
    if (this.decoder.state !== "configured") {
      this.ack(seq);
      if (!key || this.configuring) return; // 等关键帧 / 正在配置
      const sps = nal(data, 7);
      if (sps) this.configure(sps);
      return;
    }
    if (this.decoder.decodeQueueSize > 8) { // 解码跟不上：丢掉重来，要关键帧
      this.dropped++; this.ack(seq); this.decoder.reset(); this.msg(M.KEYREQ); return;
    }
    if (this.canvas.width !== w || this.canvas.height !== h) { this.canvas.width = w; this.canvas.height = h; }
    this.t0[seq % 64] = performance.now();
    try { this.decoder.decode(new EncodedVideoChunk({ type: key ? "key" : "delta", timestamp: seq, data })); }
    catch { this.ack(seq); this.decoder.reset(); this.msg(M.KEYREQ); }
  }
  // 先问浏览器支不支持硬件解码，不支持（比如虚拟机里没有显卡解码）就用软件解码
  async configure(sps) {
    this.configuring = true;
    const hex = (x) => x.toString(16).padStart(2, "0");
    const base = { codec: `avc1.${hex(sps[1])}${hex(sps[2])}${hex(sps[3])}`, optimizeForLatency: true };
    let cfg = { ...base, hardwareAcceleration: "prefer-hardware" };
    try { if (!(await VideoDecoder.isConfigSupported(cfg)).supported) cfg = base; } catch { cfg = base; }
    try {
      if (!(await VideoDecoder.isConfigSupported(cfg)).supported) throw new Error(base.codec + " not supported");
      if (!this.decoder || this.decoder.state === "closed") this.makeDecoder();
      this.decoder.configure(cfg);
      this.hw = cfg.hardwareAcceleration ? "硬件解码" : "软件解码";
    } catch (e) { $("#waiting span").textContent = T.errCodec + " (" + e.message + ")"; }
    this.configuring = false;
    this.msg(M.KEYREQ); // 配置好了：要一个新的关键帧
  }
  makeDecoder() {
    this.t0 = new Array(64).fill(0);
    this.decoder = new VideoDecoder({
      output: (frame) => {
        this.ctx.drawImage(frame, 0, 0, this.canvas.width, this.canvas.height);
        const seq = frame.timestamp;
        frame.close();
        this.frames++;
        const t0 = this.t0[seq % 64]; if (t0) this.decodeMs = this.decodeMs * 0.9 + (performance.now() - t0) * 0.1;
        if (!this.gotFrame) { this.gotFrame = true; $("#waiting").hidden = true; }
        this.ack(seq); // 显示后再确认，被控端据此控制发送节奏
      },
      error: (e) => { this.decoder = null; $("#waiting span").textContent = "decoder: " + e.message; this.msg(M.KEYREQ); },
    });
  }
  ack(seq) { const b = new Uint8Array(4); new DataView(b.buffer).setUint32(0, seq); this.msg(M.ACK, b); }

  // 兼容：被控端退回 JPEG 时
  async onJPEG(p, v) {
    const seq = v.getUint32(1), W = v.getUint16(5), H = v.getUint16(7), n = p[9];
    if (this.canvas.width !== W || this.canvas.height !== H) { this.canvas.width = W; this.canvas.height = H; }
    let o = 10;
    for (let i = 0; i < n && o + 12 <= p.length; i++) {
      const x = v.getUint16(o), y = v.getUint16(o + 2), len = v.getUint32(o + 8); o += 12;
      const bmp = await createImageBitmap(new Blob([p.subarray(o, o + len)], { type: "image/jpeg" })); o += len;
      this.ctx.drawImage(bmp, x, y); bmp.close();
    }
    this.frames++;
    if (!this.gotFrame) { this.gotFrame = true; $("#waiting").hidden = true; }
    this.ack(seq);
  }

  // ----- 声音：AAC-LC 48kHz 立体声 → AudioContext，攒 120ms 再播，积压超过 500ms 丢掉追实时 -----
  startAudio() {
    if (!("AudioDecoder" in window)) return;
    this.ac = new AudioContext({ sampleRate: 48000, latencyHint: "interactive" });
    this.gain = this.ac.createGain(); this.gain.connect(this.ac.destination);
    this.gain.gain.value = this.audioOn ? 1 : 0;
    this.playAt = 0;
    this.adec = new AudioDecoder({
      output: (a) => {
        const n = a.numberOfFrames, buf = this.ac.createBuffer(2, n, 48000);
        for (let ch = 0; ch < 2; ch++) a.copyTo(buf.getChannelData(ch), { planeIndex: Math.min(ch, a.numberOfChannels - 1), format: "f32-planar" });
        a.close();
        const now = this.ac.currentTime;
        if (this.playAt < now) this.playAt = now + 0.12; // 断粮：重新攒一点
        if (this.playAt - now > 0.5) return; // 积压太多：丢
        const src = this.ac.createBufferSource(); src.buffer = buf; src.connect(this.gain); src.start(this.playAt);
        this.playAt += n / 48000;
      },
      error: () => { this.adec = null; },
    });
    this.adec.configure({ codec: "mp4a.40.2", sampleRate: 48000, numberOfChannels: 2, description: new Uint8Array([0x11, 0x90]) });
    this.ats = 0;
  }
  onAudio(aac) {
    if (!this.adec || this.adec.state !== "configured" || !this.audioOn) return;
    if (this.ac.state === "suspended") this.ac.resume();
    this.adec.decode(new EncodedAudioChunk({ type: "key", timestamp: this.ats, data: aac }));
    this.ats += 21333;
  }
  setAudio(on) {
    this.audioOn = on; store.set("audioOn", on);
    if (this.gain) this.gain.gain.value = on ? 1 : 0;
    this.playAt = 0;
    this.msg(M.SETTINGS, this.settingsJSON());
    updateBar(this);
  }

  // ----- 光标：本机光标换成被控端当前光标的样子 -----
  onCursorShape(p, v) {
    const w = v.getUint16(1) / 100, h = v.getUint16(3) / 100, hx = v.getUint16(5) / 100, hy = v.getUint16(7) / 100;
    const img = new Image();
    img.onload = () => {
      const k = Math.min(1, 96 / Math.max(w, h)); // 浏览器光标最大 128 像素
      const c = document.createElement("canvas");
      c.width = Math.max(1, Math.round(w * k)); c.height = Math.max(1, Math.round(h * k));
      c.getContext("2d").drawImage(img, 0, 0, c.width, c.height);
      const url = c.toDataURL();
      this.canvas.style.cursor = `url(${url}) ${Math.round(hx * k)} ${Math.round(hy * k)}, default`;
      // 触屏设备没有本机光标：在画面上画一个（热点按比例换算到 24 像素高）
      this.hot = [hx / Math.max(h, 1), hy / Math.max(h, 1), w / Math.max(h, 1)];
      $("#rcursor").src = url;
      placeCursor();
    };
    img.src = URL.createObjectURL(new Blob([p.subarray(9)], { type: "image/png" }));
  }

  // 被控端光标位置（0...65535；65535,65535 = 不在这块屏幕上）
  onCursorPos(x, y) {
    if (mm.dragging || performance.now() - mm.movedAt < 300) return; // 正在用手指移光标时以本地为准，免得被旧位置拉回去
    this.off = x === 65535 && y === 65535;
    if (!this.off) { this.cx = x; this.cy = y; }
    placeCursor();
  }

  tick() {
    const b = new Uint8Array(8); new DataView(b.buffer).setFloat64(0, performance.now());
    this.msg(M.PING, b);
    const s = { fps: this.frames, rtt: Math.round(this.rtt), kbps: Math.round(this.bytes * 8 / 1000), decodeMs: Math.round(this.decodeMs * 10) / 10,
      dropped: this.dropped, mode: "浏览器 · " + (this.hw || "–"), orient: "窗口", device: navigator.userAgent.replace(/^.*?\((.*?)\).*$/, "$1").slice(0, 60),
      app: "web 0.1.0", net: "–", connect: "" };
    $("#stats").textContent = `${s.fps} fps · ${s.rtt} ms · ${s.kbps} kbps`;
    this.msg(M.STATS, enc.encode(JSON.stringify(s)));
    this.frames = 0; this.bytes = 0;
  }

  // ----- 键鼠 -----
  move(x, y) { const b = new Uint8Array(4), v = new DataView(b.buffer); v.setUint16(0, x); v.setUint16(2, y); this.msg(M.MOVE, b); }
  button(btn, down, x, y) { const b = new Uint8Array(6), v = new DataView(b.buffer); b[0] = btn; b[1] = down ? 1 : 0; v.setUint16(2, x); v.setUint16(4, y); this.msg(M.BUTTON, b); }
  wheel(dx, dy) { if (!dx && !dy) return; const b = new Uint8Array(4), v = new DataView(b.buffer); v.setInt16(0, clamp(dx)); v.setInt16(2, clamp(dy)); this.msg(M.WHEEL, b); }
  key(code, down) { if (down) this.pressed.add(code); else this.pressed.delete(code); this.msg(M.KEY, new Uint8Array([down ? 1 : 0]), enc.encode(code)); }
  tap(codes) { for (const c of codes) this.key(c, true); for (const c of [...codes].reverse()) this.key(c, false); }
  text(s) { if (s) this.msg(M.TEXT, enc.encode(s)); }
  releaseAll() { for (const c of [...this.pressed]) this.key(c, false); }

  fail(m) { this.error = m; this.close(); }
  close() { try { this.ws.close(); } catch {} this.closed(); }
  closed() {
    if (this.done) return;
    this.done = true;
    clearInterval(this.timer); clearTimeout(this.openTimer);
    try { this.decoder?.close(); } catch {}
    try { this.adec?.close(); this.ac?.close(); } catch {}
    if (S === this) S = null;
    hideRemote();
    setBusy("");
    showError(this.error || (this.connected ? T.errClosed : T.errConnect));
  }
}

const clamp = (v) => Math.max(-32000, Math.min(32000, Math.round(v)));
// Annex-B 里找某种 NAL
function nal(b, type) {
  for (let i = 0; i + 3 < b.length; i++) {
    if (b[i] === 0 && b[i + 1] === 0 && b[i + 2] === 1 && (b[i + 3] & 0x1f) === type) {
      let j = i + 3; while (j + 2 < b.length && !(b[j] === 0 && b[j + 1] === 0 && (b[j + 2] === 1 || b[j + 2] === 0))) j++;
      return b.subarray(i + 3, j);
    }
  }
  return null;
}

let toastTimer;
function toast(m) { const t = $("#toast"); t.textContent = m; t.hidden = false; clearTimeout(toastTimer); toastTimer = setTimeout(() => (t.hidden = true), 4000); }

// ---------------- 远程画面界面 ----------------
const canvas = $("#screen");

function showRemote(s) {
  s.canvas = canvas; s.ctx = canvas.getContext("2d", { alpha: false, desynchronized: true });
  $("#login").hidden = true; $("#remote").hidden = false; $("#waiting").hidden = false;
  $("#hostName").textContent = s.host.name || s.id;
  document.title = (s.host.name || s.id) + " · RDesk";
  const q = $("#quality"); q.textContent = "";
  for (const [k, label] of [["low", T.qLow], ["mid", T.qMid], ["high", T.qHigh]]) q.add(new Option(`${T.quality}：${label}`, k, false, k === s.quality));
  const ds = $("#display"); ds.textContent = "";
  for (let i = 0; i < (s.host.displays || 1); i++) ds.add(new Option(`${T.display} ${i + 1}`, i));
  ds.hidden = (s.host.displays || 1) < 2;
  const pop = $("#keysPop"); pop.textContent = "";
  const keys = s.hostMac
    ? [["kCmdTab", ["MetaLeft", "Tab"]], ["kSpot", ["MetaLeft", "Space"]], ["kMission", ["ControlLeft", "ArrowUp"]], ["kShot", ["MetaLeft", "ShiftLeft", "Digit4"]], ["kForce", ["MetaLeft", "AltLeft", "Escape"]], ["kLockM", ["ControlLeft", "MetaLeft", "KeyQ"]]]
    : [["kCAD", ["ControlLeft", "AltLeft", "Delete"]], ["kAltTab", ["AltLeft", "Tab"]], ["kWin", ["MetaLeft"]], ["kDesk", ["MetaLeft", "KeyD"]], ["kTask", ["ControlLeft", "ShiftLeft", "Escape"]], ["kLockW", ["MetaLeft", "KeyL"]]];
  for (const [k, codes] of keys) {
    const b = document.createElement("button"); b.textContent = T[k];
    b.onclick = () => { s.tap(codes); $(".menu").classList.remove("open"); canvas.focus(); };
    pop.appendChild(b);
  }
  updateBar(s);
  canvas.style.cursor = "default";
  canvas.focus();
}

function updateBar(s) {
  $("#audioBtn").textContent = s.audioOn ? "🔊 " + T.sound : "🔇 " + T.mute;
  $("#modeBtn").hidden = !COARSE;
  $("#modeBtn").textContent = touchMode === "mouse" ? "🖱 " + T.modeMouse : "👆 " + T.modeTouch;
  placeCursor();
}
$("#modeBtn").onclick = () => {
  touchMode = touchMode === "mouse" ? "touch" : "mouse"; store.set("touchMode", touchMode);
  if (S) updateBar(S);
  toast(touchMode === "mouse" ? T.mouseHint : T.touchHint);
};

function hideRemote() {
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
  $("#remote").hidden = true; $("#login").hidden = false;
  document.title = T.title;
  const c = canvas.getContext("2d"); c.clearRect(0, 0, canvas.width, canvas.height);
}

$("#audioBtn").onclick = () => S?.setAudio(!S.audioOn);
$("#keysBtn").onclick = (e) => { e.stopPropagation(); $(".menu").classList.toggle("open"); };
document.addEventListener("click", () => $(".menu").classList.remove("open"));
$("#quality").onchange = (e) => { if (!S) return; S.quality = e.target.value; store.set("quality", S.quality); S.msg(M.SETTINGS, S.settingsJSON()); canvas.focus(); };
$("#display").onchange = (e) => { if (!S) return; S.display = +e.target.value; S.msg(M.SETTINGS, S.settingsJSON()); canvas.focus(); };
$("#byeBtn").onclick = () => S?.close();
$("#pasteBtn").onclick = async () => { try { S?.text(await navigator.clipboard.readText()); } catch {} canvas.focus(); };
$("#fsBtn").onclick = async () => {
  if (document.fullscreenElement) return document.exitFullscreen();
  await $("#remote").requestFullscreen().catch(() => {});
  try { await navigator.keyboard?.lock(); } catch {} // 全屏时把 Esc、Win 等也交给远程
  toast(T.fsHint);
  canvas.focus();
};
document.addEventListener("fullscreenchange", () => { if (!document.fullscreenElement) navigator.keyboard?.unlock?.(); });
document.addEventListener("mousemove", (e) => { if (document.fullscreenElement) $("#bar").classList.toggle("show", e.clientY < 8 || $("#bar").matches(":hover")); });

// 鼠标位置 → 被控端坐标 0...65535（画面按比例居中，四周可能有黑边）
function remoteXY(e) {
  const r = canvas.getBoundingClientRect(), W = canvas.width || 1, H = canvas.height || 1;
  const s = Math.min(r.width / W, r.height / H), w = W * s, h = H * s;
  const x = (e.clientX - r.left - (r.width - w) / 2) / w, y = (e.clientY - r.top - (r.height - h) / 2) / h;
  return [Math.round(Math.min(1, Math.max(0, x)) * 65535), Math.round(Math.min(1, Math.max(0, y)) * 65535)];
}

let lastMove = 0;
canvas.addEventListener("pointermove", (e) => {
  if (!S?.connected) return;
  if (e.pointerType === "touch") return touchMove(e);
  const now = performance.now(); if (now - lastMove < 4) return; lastMove = now; // 最多 250 次/秒
  S.move(...remoteXY(e));
});
canvas.addEventListener("pointerdown", (e) => {
  if (!S?.connected) return;
  canvas.focus();
  try { canvas.setPointerCapture(e.pointerId); } catch {}
  e.preventDefault();
  if (e.pointerType === "touch") return touchDown(e);
  const [x, y] = remoteXY(e); S.move(x, y); S.button(e.button, true, x, y);
});
canvas.addEventListener("pointerup", (e) => {
  if (!S?.connected) return;
  e.preventDefault();
  if (e.pointerType === "touch") return touchUp(e, true);
  const [x, y] = remoteXY(e); S.button(e.button, false, x, y);
});
canvas.addEventListener("pointercancel", (e) => { if (S?.connected && e.pointerType === "touch") touchUp(e, false); });

// 触屏两种操作方式（和 App 一样）：鼠标模式像触控板，手指滑动移光标；触控模式点哪按哪
const COARSE = matchMedia("(pointer: coarse)").matches;
let touchMode = store.get("touchMode", "mouse");
const touchDown = (e) => (touchMode === "mouse" ? mouseDown : tapDown)(e);
const touchMove = (e) => (touchMode === "mouse" ? mouseMove : tapMove)(e);
const touchUp = (e, ok) => (touchMode === "mouse" ? mouseUp : tapUp)(e, ok);

// 远程坐标 → 屏幕位置，画触屏用的光标
function placeCursor() {
  const img = $("#rcursor");
  if (!S?.connected || !COARSE || touchMode !== "mouse" || S.off || !S.hot || $("#remote").hidden) { img.hidden = true; return; }
  const r = canvas.getBoundingClientRect(), W = canvas.width || 1, H = canvas.height || 1;
  const s = Math.min(r.width / W, r.height / H), w = W * s, h = H * s;
  const x = r.left + (r.width - w) / 2 + S.cx / 65535 * w, y = r.top + (r.height - h) / 2 + S.cy / 65535 * h;
  img.style.left = x - S.hot[0] * 24 + "px"; img.style.top = y - S.hot[1] * 24 + "px";
  img.hidden = false;
}
addEventListener("resize", placeCursor);

// 鼠标模式：单指滑 = 移光标；轻点 = 左键；按住 0.4 秒再拖 = 拖拽；双指轻点 = 右键；双指滑 = 滚动
const mm = { fingers: new Map(), id: null, sx: 0, sy: 0, lx: 0, ly: 0, t: 0, moved: false, timer: 0, dragging: false, two: 0, twoMoved: false, movedAt: 0, wx: 0, wy: 0 };
function mouseDown(e) {
  mm.fingers.set(e.pointerId, [e.clientX, e.clientY]);
  if (mm.fingers.size === 2) {
    clearTimeout(mm.timer);
    if (mm.dragging) { S.button(0, false, S.cx, S.cy); mm.dragging = false; }
    mm.two = performance.now(); mm.twoMoved = false; mm.wx = 0; mm.wy = 0;
    return;
  }
  if (mm.fingers.size > 2) return;
  mm.id = e.pointerId; mm.sx = mm.lx = e.clientX; mm.sy = mm.ly = e.clientY; mm.t = performance.now(); mm.moved = false; mm.two = 0;
  mm.timer = setTimeout(() => { // 按住不动：进入拖拽（左键按下不放）
    if (mm.moved || mm.two) return;
    mm.dragging = true; S.button(0, true, S.cx, S.cy); navigator.vibrate?.(15);
  }, 400);
}
function mouseMove(e) {
  const prev = mm.fingers.get(e.pointerId);
  if (!prev) return;
  mm.fingers.set(e.pointerId, [e.clientX, e.clientY]);
  if (mm.two) { // 双指滑动 = 滚动（手指往上推，内容往下走，和触控板一样）
    if (e.pointerId !== [...mm.fingers.keys()][0]) return;
    const dx = e.clientX - prev[0], dy = e.clientY - prev[1];
    if (Math.abs(dx) + Math.abs(dy) > 0) mm.twoMoved = mm.twoMoved || Math.hypot(dx, dy) > 2;
    mm.wx += -dx * 3; mm.wy += -dy * 3;
    const ix = Math.trunc(mm.wx), iy = Math.trunc(mm.wy);
    if (ix || iy) { S.wheel(ix, iy); mm.wx -= ix; mm.wy -= iy; }
    return;
  }
  if (e.pointerId !== mm.id) return;
  const dx = e.clientX - mm.lx, dy = e.clientY - mm.ly;
  mm.lx = e.clientX; mm.ly = e.clientY;
  if (!mm.moved && Math.hypot(e.clientX - mm.sx, e.clientY - mm.sy) < 8) return;
  if (!mm.moved) { mm.moved = true; if (!mm.dragging) clearTimeout(mm.timer); }
  // 手指移动换算成光标移动：按画面实际显示的大小，1.5 倍速，快划再加速（横竖速度一致）
  const r = canvas.getBoundingClientRect(), W = canvas.width || 1, H = canvas.height || 1;
  const sc = Math.min(r.width / W, r.height / H), gain = 1.5 * (Math.hypot(dx, dy) > 12 ? 1.8 : 1);
  S.cx = Math.max(0, Math.min(65535, S.cx + dx * gain * 65535 / (W * sc)));
  S.cy = Math.max(0, Math.min(65535, S.cy + dy * gain * 65535 / (H * sc)));
  S.off = false; mm.movedAt = performance.now();
  const now = performance.now(); if (now - lastMove >= 4) { lastMove = now; S.move(Math.round(S.cx), Math.round(S.cy)); }
  placeCursor();
}
function mouseUp(e, ok) {
  mm.fingers.delete(e.pointerId);
  if (mm.two) {
    if (mm.fingers.size === 0) {
      if (ok && !mm.twoMoved && performance.now() - mm.two < 350) { S.button(2, true, S.cx, S.cy); S.button(2, false, S.cx, S.cy); }
      mm.two = 0;
    }
    return;
  }
  if (e.pointerId !== mm.id) return;
  clearTimeout(mm.timer);
  const cx = Math.round(S.cx), cy = Math.round(S.cy);
  if (mm.dragging) { S.move(cx, cy); S.button(0, false, cx, cy); mm.dragging = false; }
  else if (ok && !mm.moved && performance.now() - mm.t < 400) { S.button(0, true, cx, cy); S.button(0, false, cx, cy); }
  mm.id = null;
}

// 触屏（手机 / 平板浏览器）：点哪按哪；按住 0.5 秒不动 = 右键；双指轻点 = 右键；单指拖 = 拖拽（和 App 的触控模式一样）
const tc = { fingers: new Set(), id: null, x: 0, y: 0, cx: 0, cy: 0, timer: 0, down: false, done: false, two: 0, twoMoved: false };
function tapDown(e) {
  tc.fingers.add(e.pointerId);
  if (tc.fingers.size === 2) { // 第二根手指：取消单指动作，准备双指轻点
    clearTimeout(tc.timer);
    if (tc.down) { S.button(0, false, tc.x, tc.y); tc.down = false; }
    tc.done = true; tc.two = performance.now(); tc.twoMoved = false;
    return;
  }
  if (tc.fingers.size > 2) return;
  [tc.x, tc.y] = remoteXY(e); tc.cx = e.clientX; tc.cy = e.clientY;
  tc.id = e.pointerId; tc.down = false; tc.done = false; tc.two = 0;
  tc.timer = setTimeout(() => { // 按住不动 = 右键
    if (tc.down || tc.done) return;
    tc.done = true;
    S.move(tc.x, tc.y); S.button(2, true, tc.x, tc.y); S.button(2, false, tc.x, tc.y);
    navigator.vibrate?.(15);
  }, 500);
}
function tapMove(e) {
  if (tc.two) { if (Math.hypot(e.clientX - tc.cx, e.clientY - tc.cy) > 12) tc.twoMoved = true; return; }
  if (e.pointerId !== tc.id || tc.done) return;
  if (!tc.down) {
    if (Math.hypot(e.clientX - tc.cx, e.clientY - tc.cy) < 10) return;
    clearTimeout(tc.timer); // 拖动了：从按下的位置开始拖拽
    S.move(tc.x, tc.y); S.button(0, true, tc.x, tc.y); tc.down = true;
  }
  const now = performance.now(); if (now - lastMove < 4) return; lastMove = now;
  [tc.x, tc.y] = remoteXY(e); S.move(tc.x, tc.y);
}
function tapUp(e, ok) {
  tc.fingers.delete(e.pointerId);
  if (tc.two) { // 双指轻点：两根手指都抬起、没怎么动、时间短
    if (tc.fingers.size === 0) {
      if (ok && !tc.twoMoved && performance.now() - tc.two < 350) { S.move(tc.x, tc.y); S.button(2, true, tc.x, tc.y); S.button(2, false, tc.x, tc.y); }
      tc.two = 0;
    }
    return;
  }
  if (e.pointerId !== tc.id) return;
  clearTimeout(tc.timer);
  if (tc.down) { S.button(0, false, tc.x, tc.y); tc.down = false; }
  else if (!tc.done && ok) { S.move(tc.x, tc.y); S.button(0, true, tc.x, tc.y); S.button(0, false, tc.x, tc.y); }
  tc.done = true;
}
canvas.addEventListener("contextmenu", (e) => e.preventDefault());
let wheelRest = [0, 0];
canvas.addEventListener("wheel", (e) => {
  if (!S?.connected) return;
  e.preventDefault();
  const k = e.deltaMode === 1 ? 40 : e.deltaMode === 2 ? 800 : 1;
  wheelRest[0] += e.deltaX * k; wheelRest[1] += e.deltaY * k;
  const dx = Math.trunc(wheelRest[0]), dy = Math.trunc(wheelRest[1]);
  wheelRest[0] -= dx; wheelRest[1] -= dy;
  S.wheel(dx, dy);
}, { passive: false });

// 键盘：直接发 KeyboardEvent.code（被控端按 code 映射）。
// 控制端是 Mac、被控端是 Windows：⌘ 当 Ctrl；控制端是 Windows/Linux、被控端是 Mac：Ctrl 当 ⌘（复制粘贴习惯不变）
function mapCode(code) {
  if (!S) return code;
  if (CLIENT_MAC && !S.hostMac) return { MetaLeft: "ControlLeft", MetaRight: "ControlRight" }[code] || code;
  if (!CLIENT_MAC && S.hostMac) return { ControlLeft: "MetaLeft", ControlRight: "MetaRight", MetaLeft: "ControlLeft", MetaRight: "ControlRight" }[code] || code;
  return code;
}
function onKey(e, down) {
  if (!S?.connected || $("#remote").hidden || !e.code) return;
  if (e.target.closest && e.target.closest("#bar")) return;
  e.preventDefault();
  if (e.code === "CapsLock") { S.tap(["CapsLock"]); return; }
  if (down && e.repeat && !S.pressed.has(mapCode(e.code))) return;
  S.key(mapCode(e.code), down);
}
window.addEventListener("keydown", (e) => onKey(e, true));
window.addEventListener("keyup", (e) => onKey(e, false));
window.addEventListener("blur", () => S?.releaseAll());
document.addEventListener("visibilitychange", () => { if (document.hidden) S?.releaseAll(); else if (S) S.msg(M.KEYREQ); });
