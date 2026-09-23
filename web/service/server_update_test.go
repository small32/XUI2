package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceXrayFilesCanRollback(t *testing.T) {
	dir := t.TempDir()
	files := []xrayUpdateFile{
		{staged: filepath.Join(dir, "new-xray"), target: filepath.Join(dir, "xray"), mode: 0755},
		{staged: filepath.Join(dir, "new-geoip"), target: filepath.Join(dir, "geoip.dat"), mode: 0644},
	}
	for _, pair := range []struct{ path, value string }{
		{files[0].target, "old xray"},
		{files[0].staged, "new xray"},
		{files[1].target, "old geoip"},
		{files[1].staged, "new geoip"},
	} {
		if err := os.WriteFile(pair.path, []byte(pair.value), 0644); err != nil {
			t.Fatal(err)
		}
	}
	backups, err := replaceXrayFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, files[0].target, "new xray")
	assertFileContent(t, files[1].target, "new geoip")
	if err := rollbackXrayFiles(backups); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, files[0].target, "old xray")
	assertFileContent(t, files[1].target, "old geoip")
}

func TestReplaceXrayFilesRestoresPartialReplacementOnFailure(t *testing.T) {
	dir := t.TempDir()
	first := xrayUpdateFile{staged: filepath.Join(dir, "new-xray"), target: filepath.Join(dir, "xray"), mode: 0755}
	second := xrayUpdateFile{staged: filepath.Join(dir, "missing-stage"), target: filepath.Join(dir, "geoip.dat"), mode: 0644}
	for path, content := range map[string]string{first.target: "old xray", first.staged: "new xray", second.target: "old geoip"} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := replaceXrayFiles([]xrayUpdateFile{first, second}); err == nil {
		t.Fatal("replacement should fail for a missing staged file")
	}
	assertFileContent(t, first.target, "old xray")
	assertFileContent(t, second.target, "old geoip")
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
