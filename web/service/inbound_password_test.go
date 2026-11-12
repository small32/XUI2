package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"xui/database"
	"xui/database/model"
)

func passwordTestInbound(protocol model.Protocol, passwords ...string) model.Inbound {
	settings := map[string]interface{}{}
	switch protocol {
	case model.Shadowsocks:
		settings["method"] = "aes-128-gcm"
		settings["password"] = passwords[0]
	case model.Trojan:
		clients := make([]map[string]string, 0, len(passwords))
		for _, password := range passwords {
			clients = append(clients, map[string]string{"password": password})
		}
		settings["clients"] = clients
	case model.Http, model.Socks:
		settings["auth"] = "password"
		accounts := make([]map[string]string, 0, len(passwords))
		for i, password := range passwords {
			accounts = append(accounts, map[string]string{"user": fmt.Sprintf("user%d", i), "pass": password})
		}
		settings["accounts"] = accounts
	default:
		settings["clients"] = []map[string]string{{"id": passwords[0]}}
	}
	raw, _ := json.Marshal(settings)
	return model.Inbound{Port: 34101, Tag: "inbound-34101", Protocol: protocol, Settings: string(raw), StreamSettings: `{}`, Sniffing: `{}`, Remark: "password-test", Enable: true}
}

func passwordTestNodeConfigs(t *testing.T, configs map[string]NodeInboundConfig) string {
	t.Helper()
	raw, err := json.Marshal(configs)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestInboundPasswordAlphabet(t *testing.T) {
	values := []struct {
		name, value string
		valid       bool
	}{
		{"letters_digits_separators", "AZaz09_-", true},
		{"single_character", "a", true},
		{"backslash", `bad\password`, false},
		{"space", "bad password", false},
		{"emoji", "bad😀password", false},
		{"newline", "bad\npassword", false},
		{"empty", "", false},
	}
	for _, protocol := range []model.Protocol{model.Shadowsocks, model.Trojan, model.Socks, model.Http} {
		for _, value := range values {
			t.Run(string(protocol)+"/"+value.name, func(t *testing.T) {
				in := passwordTestInbound(protocol, value.value)
				err := validateInboundPasswords(&in, nil)
				if value.valid && err != nil {
					t.Fatalf("allowed password rejected: %v", err)
				}
				if !value.valid && !errors.Is(err, ErrInboundPassword) {
					t.Fatalf("invalid password must return ErrInboundPassword, got %v", err)
				}
			})
		}
	}
}

func TestInboundPasswordChecksEveryAccount(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Trojan, model.Socks, model.Http} {
		t.Run(string(protocol), func(t *testing.T) {
			in := passwordTestInbound(protocol, "first_valid", "second-valid", "last invalid")
			if err := validateInboundPasswords(&in, nil); !errors.Is(err, ErrInboundPassword) {
				t.Fatalf("invalid final account accepted: %v", err)
			}
			in = passwordTestInbound(protocol, "first_valid", "second-valid", "last_valid")
			if err := validateInboundPasswords(&in, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInboundPasswordRejectsNonStringValues(t *testing.T) {
	cases := []struct {
		name     string
		protocol model.Protocol
		settings string
	}{
		{"numeric_shadowsocks", model.Shadowsocks, `{"method":"aes-128-gcm","password":123}`},
		{"null_later_account", model.Http, `{"accounts":[{"user":"first","pass":"valid"},{"user":"second","pass":null}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			previous := passwordTestInbound(tc.protocol, "")
			if tc.protocol == model.Http {
				previous = passwordTestInbound(tc.protocol, "valid", "")
			}
			in := previous
			in.Settings = tc.settings
			for _, old := range []*model.Inbound{nil, &previous} {
				if err := validateInboundPasswords(&in, old); !errors.Is(err, ErrInboundPassword) {
					t.Fatalf("non-string password accepted (existing=%v): %v", old != nil, err)
				}
			}
		})
	}
}

func TestInboundPasswordProtocolExceptions(t *testing.T) {
	for _, protocol := range []model.Protocol{model.VMess, model.VLESS} {
		t.Run(string(protocol), func(t *testing.T) {
			in := passwordTestInbound(protocol, "uuid\\ with spaces😀\n")
			if err := validateInboundPasswords(&in, nil); err != nil {
				t.Fatalf("UUID fields must not use the password alphabet: %v", err)
			}
		})
	}
	in := passwordTestInbound(model.Socks, "unused\\ password😀\n")
	in.Settings = `{"auth":"noauth","accounts":[{"user":"unused","pass":"unused\\ password😀\n"}]}`
	if err := validateInboundPasswords(&in, nil); err != nil {
		t.Fatalf("SOCKS noauth password must be ignored: %v", err)
	}
}

func TestInboundPasswordSharedAndEffectiveNodeCredentials(t *testing.T) {
	in := passwordTestInbound(model.VLESS, "uuid")
	in.SharedPassword = "Unified_123-Password"
	in.NodeConfigs = passwordTestNodeConfigs(t, map[string]NodeInboundConfig{
		"1": {Protocol: model.Trojan, Settings: `{"clients":[{"password":"unused invalid"},{"password":"unused\\invalid"}]}`, StreamSettings: `{}`, Sniffing: `{}`},
		"2": {Protocol: model.Shadowsocks, Settings: `{"method":"aes-128-gcm","password":"unused invalid"}`, StreamSettings: `{}`, Sniffing: `{}`},
		"3": {Protocol: model.Http, Settings: `{"accounts":[{"user":"user","pass":"unused invalid"}]}`, StreamSettings: `{}`, Sniffing: `{}`},
	})
	before := in
	if err := validateInboundPasswords(&in, nil); err != nil {
		t.Fatalf("valid shared password must replace stale node credentials: %v", err)
	}
	if !reflect.DeepEqual(in, before) {
		t.Fatal("password validation mutated the inbound")
	}
	in.SharedPassword = "bad unified password"
	if err := validateInboundPasswords(&in, nil); !errors.Is(err, ErrInboundPassword) {
		t.Fatalf("invalid shared password accepted: %v", err)
	}
	in.NodeConfigs = ""
	if err := validateInboundPasswords(&in, nil); !errors.Is(err, ErrInboundPassword) {
		t.Fatalf("VLESS login shared password must still be validated: %v", err)
	}
}

func TestInboundPasswordLegacyCompatibility(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Shadowsocks, model.Trojan, model.Socks, model.Http} {
		t.Run(string(protocol), func(t *testing.T) {
			previous := passwordTestInbound(protocol, "legacy\\ password😀\n")
			in := previous
			in.Remark = "edited unrelated field"
			if err := validateInboundPasswords(&in, &previous); err != nil {
				t.Fatalf("unchanged legacy password rejected: %v", err)
			}
			in.Settings = passwordTestInbound(protocol, "changed bad password").Settings
			if err := validateInboundPasswords(&in, &previous); !errors.Is(err, ErrInboundPassword) {
				t.Fatalf("changed invalid password accepted: %v", err)
			}
			in.Settings = passwordTestInbound(protocol, "replacement_valid-123").Settings
			if err := validateInboundPasswords(&in, &previous); err != nil {
				t.Fatalf("valid replacement rejected: %v", err)
			}
		})
	}
	previous := passwordTestInbound(model.Trojan, "first_good", "legacy second password")
	in := passwordTestInbound(model.Trojan, "legacy second password", "first_good")
	if err := validateInboundPasswords(&in, &previous); !errors.Is(err, ErrInboundPassword) {
		t.Fatalf("legacy password moved into another account slot: %v", err)
	}
}

func TestInboundPasswordLegacySharedInheritance(t *testing.T) {
	for _, explicitShared := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit_shared_%v", explicitShared), func(t *testing.T) {
			previous := passwordTestInbound(model.Shadowsocks, "legacy shared\\ password")
			if explicitShared {
				previous.SharedPassword = "legacy shared\\ password"
			}
			in := passwordTestInbound(model.Trojan, "legacy shared\\ password", "legacy shared\\ password")
			in.SharedPassword = "legacy shared\\ password"
			in.NodeConfigs = passwordTestNodeConfigs(t, map[string]NodeInboundConfig{
				"7": {Protocol: model.Socks, Settings: `{"auth":"password","accounts":[{"user":"user","pass":"placeholder"}]}`, StreamSettings: `{}`, Sniffing: `{}`},
			})
			if err := validateInboundPasswords(&in, &previous); err != nil {
				t.Fatalf("unchanged effective shared password cannot cross protocol/node: %v", err)
			}
			in.SharedPassword = "changed invalid shared"
			in.Settings = passwordTestInbound(model.Trojan, in.SharedPassword).Settings
			if err := validateInboundPasswords(&in, &previous); !errors.Is(err, ErrInboundPassword) {
				t.Fatalf("changed shared password accepted: %v", err)
			}
		})
	}
}

func passwordTestStored(t *testing.T, id int) model.Inbound {
	t.Helper()
	var in model.Inbound
	if err := database.GetDB().First(&in, id).Error; err != nil {
		t.Fatal(err)
	}
	return in
}

func passwordTestCount(t *testing.T, want int64) {
	t.Helper()
	var count int64
	if err := database.GetDB().Model(&model.Inbound{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("inbound row count = %d, want %d", count, want)
	}
}

func TestInboundPasswordAddAndBulkWrites(t *testing.T) {
	clientTestDB(t)
	s := new(InboundService)
	bad := passwordTestInbound(model.Shadowsocks, "bad\\password")
	if err := s.AddInbound(&bad); !errors.Is(err, ErrInboundPassword) {
		t.Fatalf("AddInbound accepted invalid password: %v", err)
	}
	passwordTestCount(t, 0)
	good := passwordTestInbound(model.Trojan, "valid_123")
	bad = passwordTestInbound(model.Http, "first_valid", "second invalid")
	bad.Port, bad.Tag = 34102, "inbound-34102"
	if err := s.AddInbounds([]*model.Inbound{&good, &bad}); !errors.Is(err, ErrInboundPassword) {
		t.Fatalf("AddInbounds accepted invalid later account: %v", err)
	}
	passwordTestCount(t, 0)
	if good.Id != 0 {
		t.Fatal("failed batch partially saved its first inbound")
	}
	bad.Settings = passwordTestInbound(model.Http, "first_valid", "second-valid").Settings
	if err := s.AddInbounds([]*model.Inbound{&good, &bad}); err != nil {
		t.Fatalf("valid batch rejected: %v", err)
	}
	passwordTestCount(t, 2)
	if passwordTestStored(t, bad.Id).Settings != bad.Settings {
		t.Fatal("valid batch password was not persisted")
	}
	shared := passwordTestInbound(model.VLESS, "uuid")
	shared.Port, shared.Tag, shared.SharedPassword = 34103, "inbound-34103", "bad login password"
	if err := s.AddInbound(&shared); !errors.Is(err, ErrInboundPassword) {
		t.Fatalf("AddInbound accepted invalid VLESS login password: %v", err)
	}
	passwordTestCount(t, 2)
	shared.SharedPassword = "valid_login-123"
	if err := s.AddInbound(&shared); err != nil {
		t.Fatalf("valid AddInbound rejected: %v", err)
	}
	passwordTestCount(t, 3)
}

func TestInboundPasswordUpdatePreservesLegacyAndRejectsChanges(t *testing.T) {
	clientTestDB(t)
	legacy := passwordTestInbound(model.Trojan, "legacy\\ password")
	legacy.Up, legacy.Down = 17, 29
	if err := database.GetDB().Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	s := new(InboundService)
	edit := legacy
	edit.Remark = "unrelated edit"
	if err := s.UpdateInbound(&edit); err != nil {
		t.Fatalf("unrelated legacy edit rejected: %v", err)
	}
	before := passwordTestStored(t, legacy.Id)
	edit.Settings = passwordTestInbound(model.Trojan, "new invalid password").Settings
	edit.Remark = "must not persist"
	if err := s.UpdateInbound(&edit); !errors.Is(err, ErrInboundPassword) {
		t.Fatalf("invalid password update accepted: %v", err)
	}
	if after := passwordTestStored(t, legacy.Id); !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected update changed the stored row: before=%+v after=%+v", before, after)
	}
	edit.Settings = passwordTestInbound(model.Trojan, "new_valid-password").Settings
	if err := s.UpdateInbound(&edit); err != nil {
		t.Fatalf("valid password replacement rejected: %v", err)
	}
	after := passwordTestStored(t, legacy.Id)
	if after.Settings != edit.Settings || after.Up != legacy.Up || after.Down != legacy.Down {
		t.Fatalf("valid replacement not persisted or traffic changed: %+v", after)
	}
}

func TestInboundPasswordImportedAndManagedWrites(t *testing.T) {
	writers := []struct {
		name  string
		write func(model.Inbound) error
	}{
		{"import", func(in model.Inbound) error { return ImportNodeInbound(in, true) }},
		{"managed", UpsertManagedInbound},
	}
	for _, writer := range writers {
		t.Run(writer.name, func(t *testing.T) {
			clientTestDB(t)
			in := passwordTestInbound(model.Shadowsocks, "legacy\\ password")
			in.ManagerAccountID = 42
			if err := writer.write(in); !errors.Is(err, ErrInboundPassword) {
				t.Fatalf("new target accepted invalid source password: %v", err)
			}
			passwordTestCount(t, 0)
			in.ManagerAccountID = 0
			in.Up, in.Down = 31, 47
			if err := database.GetDB().Create(&in).Error; err != nil {
				t.Fatal(err)
			}
			request := in
			request.ManagerAccountID = 42
			request.Remark = "legacy replay"
			for i := 0; i < 2; i++ {
				if err := writer.write(request); err != nil {
					t.Fatalf("unchanged legacy replay rejected: %v", err)
				}
			}
			before := passwordTestStored(t, in.Id)
			if before.Remark != request.Remark || before.Up != in.Up || before.Down != in.Down {
				t.Fatalf("legacy replay lost metadata/traffic: %+v", before)
			}
			request.Settings = passwordTestInbound(model.Shadowsocks, "changed invalid password").Settings
			request.Remark = "must not persist"
			if err := writer.write(request); !errors.Is(err, ErrInboundPassword) {
				t.Fatalf("invalid replacement accepted: %v", err)
			}
			if after := passwordTestStored(t, in.Id); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected write modified the legacy row: before=%+v after=%+v", before, after)
			}
			request.Settings = passwordTestInbound(model.Shadowsocks, "new_valid-password").Settings
			if err := writer.write(request); err != nil {
				t.Fatalf("valid replacement rejected: %v", err)
			}
			if after := passwordTestStored(t, in.Id); after.Settings != request.Settings || after.Up != in.Up || after.Down != in.Down {
				t.Fatalf("valid replacement lost settings/traffic: %+v", after)
			}
			passwordTestCount(t, 1)
		})
	}
}

func TestInboundPasswordImportNewTargetDoesNotAlterSource(t *testing.T) {
	clientTestDB(t)
	source := passwordTestInbound(model.Shadowsocks, "legacy\\ source password")
	if err := database.GetDB().Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	before := passwordTestStored(t, source.Id)
	copy := source
	copy.Port = 34102
	if err := ImportNodeInbound(copy, true); !errors.Is(err, ErrInboundPassword) {
		t.Fatalf("copy to new target inherited source exemption: %v", err)
	}
	passwordTestCount(t, 1)
	if after := passwordTestStored(t, source.Id); !reflect.DeepEqual(after, before) {
		t.Fatal("rejected import modified its source")
	}
}

func TestInboundPasswordManagedRejectedMovePreservesBothRows(t *testing.T) {
	clientTestDB(t)
	managed := passwordTestInbound(model.Trojan, "legacy managed password")
	managed.ManagerAccountID, managed.Up, managed.Down = 42, 11, 13
	occupied := passwordTestInbound(model.Shadowsocks, "occupied_valid")
	occupied.Port, occupied.Tag, occupied.Up, occupied.Down = 34102, "inbound-34102", 17, 19
	for _, in := range []*model.Inbound{&managed, &occupied} {
		if err := database.GetDB().Create(in).Error; err != nil {
			t.Fatal(err)
		}
	}
	managedBefore, occupiedBefore := passwordTestStored(t, managed.Id), passwordTestStored(t, occupied.Id)
	request := managed
	request.Port = occupied.Port
	request.Settings = passwordTestInbound(model.Trojan, "changed invalid password").Settings
	if err := UpsertManagedInbound(request); !errors.Is(err, ErrInboundPassword) {
		t.Fatalf("managed move accepted invalid password: %v", err)
	}
	passwordTestCount(t, 2)
	if !reflect.DeepEqual(passwordTestStored(t, managed.Id), managedBefore) || !reflect.DeepEqual(passwordTestStored(t, occupied.Id), occupiedBefore) {
		t.Fatal("rejected managed move deleted a row or changed counters/configuration")
	}
}
