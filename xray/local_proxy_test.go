package xray

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLocalProxyUsesManualRoutingWithoutChangingUpstream(t *testing.T) {
	base := `{"api":{"tag":"api"},"inbounds":[{"tag":"api","port":62789}],"outbounds":[{"protocol":"freedom","tag":"direct"},{"protocol":"trojan","tag":"to-b","settings":{"servers":[{"address":"example.com","port":443,"password":"keep"}]}}],"routing":{"rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"},{"type":"field","network":"tcp,udp","outboundTag":"to-b"}]}}`
	generated, err := BuildLocalProxyTemplate(base, true)
	if err != nil {
		t.Fatal(err)
	}
	again, err := BuildLocalProxyTemplate(generated, true)
	if err != nil || again != generated {
		t.Fatal("generation not idempotent", err)
	}
	var a, b map[string]json.RawMessage
	json.Unmarshal([]byte(base), &a)
	json.Unmarshal([]byte(generated), &b)
	for _, key := range []string{"outbounds", "routing", "api"} {
		var x, y interface{}
		json.Unmarshal(a[key], &x)
		json.Unmarshal(b[key], &y)
		aa, _ := json.Marshal(x)
		bb, _ := json.Marshal(y)
		if string(aa) != string(bb) {
			t.Fatal("manual config changed", key)
		}
	}
	var inbounds []map[string]interface{}
	json.Unmarshal(b["inbounds"], &inbounds)
	if len(inbounds) != 3 {
		t.Fatal("local proxy listeners missing")
	}
	for _, in := range inbounds[1:] {
		if in["listen"] != "127.0.0.1" {
			t.Fatal("local proxy exposed to network")
		}
	}
	disabled, err := BuildLocalProxyTemplate(generated, false)
	if err != nil {
		t.Fatal(err)
	}
	var old, new interface{}
	json.Unmarshal([]byte(base), &old)
	json.Unmarshal([]byte(disabled), &new)
	aa, _ := json.Marshal(old)
	bb, _ := json.Marshal(new)
	if string(aa) != string(bb) {
		t.Fatal("disable changed manual configuration")
	}
	unchanged, err := BuildLocalProxyTemplate(base, false)
	if err != nil || unchanged != base {
		t.Fatal("disabled proxy rewrote original template")
	}
}

func TestLocalProxyRemovesLegacyRouteOverrideButRetainsUserOutbounds(t *testing.T) {
	base := `{"inbounds":[{"tag":"xui-local-http","port":10809}],"outbounds":[{"tag":"xui-upstream","protocol":"trojan"}],"routing":{"rules":[{"inboundTag":["xui-local-http"],"outboundTag":"xui-upstream","type":"field"},{"inboundTag":["xui-local-socks","existing"],"outboundTag":"xui-upstream","type":"field"},{"network":"tcp,udp","outboundTag":"xui-upstream","type":"field"}]}}`
	result, err := BuildLocalProxyTemplate(base, true)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]interface{}
	json.Unmarshal([]byte(result), &root)
	rules := root["routing"].(map[string]interface{})["rules"].([]interface{})
	if len(rules) != 2 || rules[0].(map[string]interface{})["inboundTag"].([]interface{})[0] != "existing" {
		t.Fatal("legacy local override retained or user rule lost")
	}
	if !strings.Contains(result, "xui-upstream") {
		t.Fatal("existing upper outbound removed")
	}
	if _, err := BuildLocalProxyTemplate(`{"inbounds":[{"port":10809}],"outbounds":[{"protocol":"trojan"}]}`, true); err == nil {
		t.Fatal("port conflict accepted")
	}
	if _, err := BuildLocalProxyTemplate(`{"outbounds":[{"protocol":"freedom"}]}`, true); err == nil {
		t.Fatal("missing upstream accepted")
	}
}
