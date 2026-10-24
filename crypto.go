package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

// 端到端加密：密钥 = PBKDF2-SHA256(密码, "rdesk:"+设备ID)，AES-256-GCM。
// 中继服务器只转发密文，看不到画面和键鼠内容。
// 浏览器端 (web/index.html) 用 WebCrypto 做完全相同的推导。

const kdfIter = 150000

// 帧格式: [0x01][12字节 nonce][密文]   (0x00 开头 = 明文控制消息，只用于"密码错误")
const (
	frameEnc   = 0x01
	framePlain = 0x00
)

var (
	aadH2V = []byte("h2v") // 被控端 -> 控制端
	aadV2H = []byte("v2h") // 控制端 -> 被控端
)

type box struct{ aead cipher.AEAD }

func newBox(password, id string) (*box, error) {
	key, err := pbkdf2.Key(sha256.New, password, []byte("rdesk:"+id), kdfIter, 32)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &box{g}, nil
}

func (b *box) seal(plain, aad []byte) []byte {
	out := make([]byte, 1+12, 1+12+len(plain)+16)
	out[0] = frameEnc
	rand.Read(out[1:13])
	return b.aead.Seal(out, out[1:13], plain, aad)
}

func (b *box) open(msg, aad []byte) ([]byte, error) {
	if len(msg) < 1+12+16 || msg[0] != frameEnc {
		return nil, errors.New("bad frame")
	}
	return b.aead.Open(nil, msg[1:13], msg[13:], aad)
}
