package main

import (
	"strings"
	"testing"
)

// 每条翻译的格式符要和中文原文一致，否则 Printf 会出错
func TestI18nFormatVerbs(t *testing.T) {
	verbs := func(s string) string {
		var b strings.Builder
		for i := 0; i < len(s)-1; i++ {
			if s[i] == '%' {
				b.WriteByte(s[i+1])
				i++
			}
		}
		return b.String()
	}
	for zh, tr := range i18nTable {
		for i, s := range tr {
			if s != "" && verbs(s) != verbs(zh) {
				t.Errorf("列 %d %q: %q", i, zh, s)
			}
		}
	}
}
