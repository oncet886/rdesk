#!/bin/sh
# RDesk 中继服务器一键安装（Linux + systemd）。自动申请 HTTPS 证书，开机自启。
# One-line install of the RDesk relay server (Linux + systemd), with automatic HTTPS.
#   curl -fsSL https://rdeskapp.com/relay.sh | sudo sh -s -- relay.example.com
# 先把域名解析到这台服务器，并放开 80 和 443 端口。再次运行会升级到最新版，密钥不变。
set -e
DOMAIN="$1"
if [ -z "$DOMAIN" ]; then
  echo "用法 / Usage: curl -fsSL https://rdeskapp.com/relay.sh | sudo sh -s -- relay.example.com"
  exit 1
fi
if [ "$(id -u)" != 0 ]; then echo "请用 sudo 运行 / Please run with sudo"; exit 1; fi
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "不支持的架构 / Unsupported architecture: $(uname -m)"; exit 1 ;;
esac

echo "下载 RDesk 中继 / Downloading RDesk relay ($ARCH)…"
curl -fsSL -o /usr/local/bin/rdesk.new "https://github.com/oncet886/rdesk/releases/latest/download/rdesk-linux-$ARCH"
chmod 755 /usr/local/bin/rdesk.new
mv /usr/local/bin/rdesk.new /usr/local/bin/rdesk

# 注册密钥：只有知道它的被控端才能注册到这台中继；已有就沿用
mkdir -p /etc/rdesk
if [ ! -s /etc/rdesk/relay.env ]; then
  KEY=$(head -c 18 /dev/urandom | od -An -tx1 | tr -d ' \n')
  umask 077
  printf 'RDESK_DOMAIN=%s\nRDESK_KEY=%s\n' "$DOMAIN" "$KEY" > /etc/rdesk/relay.env
else
  sed -i "s/^RDESK_DOMAIN=.*/RDESK_DOMAIN=$DOMAIN/" /etc/rdesk/relay.env
fi
. /etc/rdesk/relay.env

cat > /etc/systemd/system/rdesk-relay.service <<'EOF'
[Unit]
Description=RDesk relay server
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/rdesk/relay.env
ExecStart=/usr/local/bin/rdesk relay -domain ${RDESK_DOMAIN} -key ${RDESK_KEY} -cert-dir /var/lib/rdesk-relay
DynamicUser=yes
StateDirectory=rdesk-relay
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable rdesk-relay >/dev/null 2>&1
systemctl restart rdesk-relay

cat <<EOF

✅ RDesk 中继已启动 / RDesk relay is running

  服务器网址 / Server address:  https://$RDESK_DOMAIN
  注册密钥   / Registration key: $RDESK_KEY

在电脑的 RDesk Host 控制面板 →「自己的中继服务器」填入上面两项，保存并重启。
手机 App 的地址填 https://$RDESK_DOMAIN ，设备 ID 和密码照旧。
In RDesk Host's control panel, open "Your own relay server", enter both values and save.
In the app, use https://$RDESK_DOMAIN as the address, with the usual device ID and password.

查看日志 / Logs:  journalctl -u rdesk-relay -f
EOF
