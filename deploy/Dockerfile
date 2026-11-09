# RDesk 中继服务器镜像（只跑中继，不含被控端）
#   docker build -t rdesk-relay -f deploy/Dockerfile https://github.com/oncet886/rdesk.git
#   docker run -d --name rdesk-relay --restart always -p 80:80 -p 443:443 -v rdesk-relay:/data \
#     rdesk-relay -domain relay.example.com -key 你的密钥
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /rdesk .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /rdesk /usr/local/bin/rdesk
VOLUME /data
EXPOSE 80 443
ENTRYPOINT ["rdesk", "relay", "-cert-dir", "/data"]
