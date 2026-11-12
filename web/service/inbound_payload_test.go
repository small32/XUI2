package service

import (
	"strings"
	"testing"

	"xui/database/model"
)

// 入站的 settings/streamSettings/sniffing 会被原样拼进 xray 配置，
// 非法 JSON 会让该被控端所有入站起不来，必须在保存前拦住。
func TestValidateInboundPayload(t *testing.T) {
	base := model.Inbound{
		Settings:       `{"method":"aes-128-gcm","password":"pw"}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
		Sniffing:       `{}`,
	}
	if err := ValidateInboundPayload(&base); err != nil {
		t.Fatalf("合法入站被拒绝: %v", err)
	}

	cases := []struct {
		name  string
		mutate func(*model.Inbound)
		want  string
	}{
		{"settings 非法", func(in *model.Inbound) { in.Settings = `{"method":` }, "settings"},
		{"streamSettings 非法", func(in *model.Inbound) { in.StreamSettings = `network:tcp` }, "streamSettings"},
		{"sniffing 非法", func(in *model.Inbound) { in.Sniffing = `{` }, "sniffing"},
		{"监听地址带引号", func(in *model.Inbound) { in.Listen = `0.0.0.0", "port": 1` }, "监听地址"},
		{"监听地址带反斜杠", func(in *model.Inbound) { in.Listen = `0.0.0.0\` }, "监听地址"},
	}
	for _, tc := range cases {
		in := base
		tc.mutate(&in)
		err := ValidateInboundPayload(&in)
		if err == nil {
			t.Fatalf("%s: 应当被拒绝", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: 错误信息不明确: %v", tc.name, err)
		}
	}

	// 留空表示沿用模型默认，不当作非法输入
	empty := model.Inbound{}
	if err := ValidateInboundPayload(&empty); err != nil {
		t.Fatalf("空字段不应被拒绝: %v", err)
	}
}
