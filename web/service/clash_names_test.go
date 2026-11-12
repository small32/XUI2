package service

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
	"xui/database"
	"xui/database/model"
)

var testClashReservedNames = []string{"DIRECT", "REJECT", "REJECT-DROP", "COMPATIBLE", "PASS", "PASS-RULE", "GLOBAL"}

func TestNodeNamePolicyOnCreateAndRename(t *testing.T) {
	clientTestDB(t)
	s := new(ServerManagementService)
	input := NodeInput{URL: "https://example.com", Address: "example.com", Token: strings.Repeat("t", 64)}
	for _, name := range []string{"A", "node-01_US", "aZ09_-", "direct", "global"} {
		input.Name = name
		if err := s.SaveNode(input); err != nil {
			t.Fatalf("valid name %q rejected: %v", name, err)
		}
	}
	invalid := append([]string{"", "node one", "node,one", "node.one", "中文", "node\\one", "node\none", "node\n", "node\t", " node", "node ", "🚀"}, testClashReservedNames...)
	for _, name := range invalid {
		input.Name = name
		if err := s.SaveNode(input); err == nil {
			t.Fatalf("invalid new name %q accepted", name)
		}
	}
	var nodes []model.ManagedNode
	if err := database.GetDB().Order("id").Find(&nodes).Error; err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 5 {
		t.Fatalf("invalid creation changed node count: %d", len(nodes))
	}
	input.ID = nodes[0].Id
	for _, name := range invalid {
		input.Name = name
		if err := s.SaveNode(input); err == nil {
			t.Fatalf("invalid renamed node %q accepted", name)
		}
	}
	var saved model.ManagedNode
	if err := database.GetDB().First(&saved, input.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Name != "A" {
		t.Fatalf("rejected rename changed stored name: %q", saved.Name)
	}
	input.Name = "renamed_Node-1"
	if err := s.SaveNode(input); err != nil {
		t.Fatal(err)
	}
}

func TestNodeNamePolicyPreservesUnchangedLegacyName(t *testing.T) {
	clientTestDB(t)
	s := new(ServerManagementService)
	for _, name := range []string{"old name, one", "中文节点", "DIRECT", "REJECT-DROP", "", " \n"} {
		node := model.ManagedNode{Name: name, URL: "https://example.com", Address: "old.example.com", Token: strings.Repeat("t", 64)}
		if err := database.GetDB().Create(&node).Error; err != nil {
			t.Fatal(err)
		}
		input := NodeInput{ID: node.Id, Name: name, URL: node.URL, Address: "new.example.com", TrafficMultiplier: multiplier(2)}
		if err := s.SaveNode(input); err != nil {
			t.Fatalf("unchanged legacy name %q rejected: %v", name, err)
		}
		var saved model.ManagedNode
		if err := database.GetDB().First(&saved, node.Id).Error; err != nil {
			t.Fatal(err)
		}
		if saved.Name != name || saved.Address != input.Address || nodeTrafficMultiplier(saved) != 2 {
			t.Fatalf("legacy edit not preserved: %+v", saved)
		}
		input.Name = "different invalid name"
		if err := s.SaveNode(input); err == nil {
			t.Fatal("legacy exemption accepted a changed invalid name")
		}
		input.Name = fmt.Sprintf("valid-%d", node.Id)
		if err := s.SaveNode(input); err != nil {
			t.Fatalf("legacy rename to valid name failed: %v", err)
		}
	}
}

func TestClashNamesAvoidLegacyReservedAndRepeatedSuffixes(t *testing.T) {
	clientTestDB(t)
	SetSubscriptionRules([]byte(testSubscriptionRules))
	names := []string{"node · 5", "node · 5 (2)", "node", "DIRECT", "node", "XUI代理", subGroupProxy, subGroupAuto, subGroupDirect, subGroupBlock, subGroupFinal}
	names = append(names, testClashReservedNames...)
	for i, name := range names {
		node := model.ManagedNode{Id: i + 1, Name: name, Address: "node.example.com", Enabled: true}
		if err := database.GetDB().Create(&node).Error; err != nil {
			t.Fatal(err)
		}
	}
	in := &model.Inbound{Id: 99, Port: 8443, Protocol: model.Shadowsocks, Settings: `{"method":"aes-128-gcm","password":"secret"}`, StreamSettings: `{}`}
	for _, generator := range []struct {
		name string
		call func(*model.Inbound, string) ([]byte, error)
	}{
		{"client API", new(ClientService).ClashConfig},
		{"subscription", new(SubscriptionService).ClashConfig},
	} {
		t.Run(generator.name, func(t *testing.T) {
			data, err := generator.call(in, "https://manager.example.com")
			if err != nil {
				t.Fatal(err)
			}
			var cfg struct {
				Proxies []struct{ Name string } `yaml:"proxies"`
				Groups  []struct {
					Name    string
					Proxies []string
				} `yaml:"proxy-groups"`
			}
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			if len(cfg.Proxies) != len(names) {
				t.Fatalf("lost proxies: %d", len(cfg.Proxies))
			}
			used := map[string]bool{}
			for _, reserved := range testClashReservedNames {
				used[reserved] = true
			}
			for _, group := range cfg.Groups {
				if used[group.Name] {
					t.Fatalf("duplicate group name %q", group.Name)
				}
				used[group.Name] = true
			}
			for _, proxy := range cfg.Proxies {
				if used[proxy.Name] {
					t.Fatalf("duplicate or reserved proxy name %q", proxy.Name)
				}
				used[proxy.Name] = true
			}
			if cfg.Proxies[4].Name != "node · 5 (3)" {
				t.Fatalf("suffix collision not resolved: %q", cfg.Proxies[4].Name)
			}
			for _, group := range cfg.Groups {
				for _, target := range group.Proxies {
					if !used[target] {
						t.Fatalf("missing proxy target %q", target)
					}
				}
			}
			again, err := generator.call(in, "https://manager.example.com")
			if err != nil || !reflect.DeepEqual(data, again) {
				t.Fatalf("names are not deterministic: %v", err)
			}
		})
	}
	var stored []model.ManagedNode
	if err := database.GetDB().Order("id").Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	for i := range stored {
		if stored[i].Name != names[i] {
			t.Fatalf("subscription changed stored node %d", stored[i].Id)
		}
	}
}

func TestClashNamesAgentRemarkAvoidsReservedAndGroups(t *testing.T) {
	t.Setenv("XUI_ROLE", "agent")
	SetSubscriptionRules([]byte(testSubscriptionRules))
	names := append([]string{subGroupProxy, subGroupAuto, subGroupDirect, subGroupBlock, subGroupFinal}, testClashReservedNames...)
	for _, name := range names {
		in := &model.Inbound{Id: 7, Remark: name, Port: 8443, Protocol: model.Shadowsocks, Settings: `{"method":"aes-128-gcm","password":"secret"}`, StreamSettings: `{}`}
		data, err := new(SubscriptionService).ClashConfig(in, "node.example.com")
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Proxies []struct{ Name string } `yaml:"proxies"`
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
		if len(cfg.Proxies) != 1 || cfg.Proxies[0].Name != name+" · 7" {
			t.Fatalf("agent remark %q was not escaped: %s", name, data)
		}
		if in.Remark != name {
			t.Fatal("subscription modified the inbound remark")
		}
	}
}
