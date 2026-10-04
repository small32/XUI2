package xray

import (
	"encoding/json"
	"strings"
	"testing"
)

const testTemplate = `{"api":{"tag":"api"},"inbounds":[{"tag":"api","port":62789,"protocol":"dokodemo-door","settings":{"address":"127.0.0.1"}},{"tag":"existing","port":12345,"protocol":"shadowsocks","settings":{"password":"keep"}}],"outbounds":[{"protocol":"freedom","tag":"direct"},{"protocol":"trojan","tag":"old-upstream"}],"routing":{"domainStrategy":"IPIfNonMatch","rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"},{"type":"field","ip":["geoip:private"],"outboundTag":"blocked"},{"type":"field","network":"tcp,udp","outboundTag":"old-upstream"}]},"dns":{"servers":["localhost"]}}`

func TestUpstreamTemplatePreservesExistingConfigAndIsReversible(t *testing.T) {
	u := `{"protocol":"trojan","address":"example.com","port":443,"password":"secret","streamSettings":{"network":"tcp","security":"tls"}}`
	generated, err := BuildUpstreamTemplate(testTemplate, u, true, true)
	if err != nil {
		t.Fatal(err)
	}
	again, err := BuildUpstreamTemplate(generated, u, true, true)
	if err != nil || again != generated {
		t.Fatalf("generation is not idempotent: %v", err)
	}
	var root map[string]interface{}
	json.Unmarshal([]byte(generated), &root)
	in := root["inbounds"].([]interface{})
	if len(in) != 4 {
		t.Fatalf("inbounds: %v", in)
	}
	if in[1].(map[string]interface{})["settings"].(map[string]interface{})["password"] != "keep" {
		t.Fatal("existing password changed")
	}
	rules := root["routing"].(map[string]interface{})["rules"].([]interface{})
	if rules[0].(map[string]interface{})["outboundTag"] != UpstreamTag || rules[1].(map[string]interface{})["outboundTag"] != "api" || rules[3].(map[string]interface{})["outboundTag"] != UpstreamTag {
		t.Fatalf("routing precedence: %v", rules)
	}
	disabled, err := BuildUpstreamTemplate(generated, u, false, false)
	if err != nil {
		t.Fatal(err)
	}
	var old, restored interface{}
	json.Unmarshal([]byte(testTemplate), &old)
	json.Unmarshal([]byte(disabled), &restored)
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(restored)
	if string(a) != string(b) {
		t.Fatalf("disable did not restore original: %s", disabled)
	}
}

func TestUpstreamProtocolsAndValidation(t *testing.T) {
	for _, protocol := range []string{"trojan", "vmess", "vless", "dokodemo-door"} {
		data, _ := json.Marshal(Upstream{Protocol: protocol, Address: "2001:db8::1", Port: 443, Password: "secret", ID: "1ea286ce-843a-4ca1-a2fd-ded394128722", StreamSettings: json.RawMessage(`{}`)})
		generated, err := BuildUpstreamTemplate(testTemplate, string(data), true, protocol != "dokodemo-door")
		if err != nil {
			t.Fatalf("%s: %v", protocol, err)
		}
		if protocol == "dokodemo-door" && !strings.Contains(generated, `[2001:db8::1]:443`) {
			t.Fatal("IPv6 tunnel target not preserved")
		}
	}
	for _, bad := range []string{`{}`, `{"protocol":"trojan","address":"a","port":443}`, `{"protocol":"vless","address":"a","port":70000,"id":"uuid"}`, `{"protocol":"http","address":"a","port":443}`, `{"protocol":"trojan","address":"a","port":443,"password":"p","streamSettings":[]}`} {
		if _, err := BuildUpstreamTemplate(testTemplate, bad, true, false); err == nil {
			t.Fatalf("invalid upstream accepted: %s", bad)
		}
	}
	if _, err := BuildUpstreamTemplate(testTemplate, `{"protocol":"dokodemo-door","address":"a","port":443}`, true, true); err == nil {
		t.Fatal("fixed tunnel accepted as system proxy")
	}
	if _, err := BuildUpstreamTemplate(testTemplate, "", false, true); err == nil {
		t.Fatal("system proxy without upstream accepted")
	}
	conflict := strings.Replace(testTemplate, "12345", "10809", 1)
	if _, err := BuildUpstreamTemplate(conflict, `{"protocol":"trojan","address":"a","port":443,"password":"p"}`, true, true); err == nil {
		t.Fatal("port conflict accepted")
	}
}
