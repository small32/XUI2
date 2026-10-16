package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"xui/database"
	"xui/database/model"
)

func TestClientGitHubPrivateTokenIsMaskedAndPreserved(t *testing.T) {
	clientTestDB(t)
	s := new(ClientService)
	token := "github_pat_private_test"
	settings := &ClientSettings{Enabled: true, GitHubReleaseEnable: true, GitHubReleaseURL: "https://github.com/small32/SClash/releases/latest"}
	if err := s.SaveSettings(settings, &token); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings()
	if err != nil || got.GitHubToken != token || !got.GitHubTokenConfigured || got.GitHubReleaseURL != "https://github.com/small32/SClash/releases/" {
		t.Fatalf("settings: configured=%v %v", got.GitHubTokenConfigured, err)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), token) || strings.Contains(string(data), `"githubToken":`) {
		t.Fatal("PAT exposed to browser")
	}
	var row model.Setting
	database.GetDB().Where("key = ?", clientSettingsKey).First(&row)
	if !strings.Contains(row.Value, token) {
		t.Fatal("PAT not persisted server-side")
	}
	got.GitHubToken = ""
	if err := s.SaveSettings(got); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Settings()
	if got.GitHubToken != token {
		t.Fatal("blank browser field erased saved token")
	}
	empty := ""
	if err := s.SaveSettings(got, &empty); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Settings()
	if got.GitHubTokenConfigured || got.GitHubToken != "" {
		t.Fatal("explicit token clear failed")
	}
	for _, bad := range []string{"https://evil.example/owner/repo/releases/", "https://github.com/../repo/releases/", "https://github.com/user/repo/issues", "https://user:pass@github.com/user/repo/releases/"} {
		if _, err := parseClientGitHubRepo(bad); err == nil {
			t.Fatalf("invalid source accepted: %s", bad)
		}
	}
}
func TestClientGitHubPrivateReleaseAssets(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			w.WriteHeader(404)
			return
		}
		if r.URL.Path != "/repos/small32/SClash/releases/latest" {
			t.Errorf("wrong API path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tag_name":"v0.9.0","html_url":"https://github.com/small32/SClash/releases/tag/v0.9.0","assets":[{"id":1,"name":"SClash-0.9.0-windows-amd64-setup.exe","size":123,"state":"uploaded","digest":"sha256:abcd"},{"id":2,"name":"SClash-0.9.0-macos-arm64.dmg","size":124},{"id":3,"name":"SClash-0.9.0-android-arm64-v8a.apk","size":125},{"id":4,"name":"SHA256SUMS","size":126},{"id":5,"name":"SClash-0.9.0-windows-amd64.zip","size":127},{"id":6,"name":"SClash-0.9.0-android-armeabi-v7a.apk","size":128}]}`)
	}))
	defer server.Close()
	result, err := fetchClientGitHubRelease(context.Background(), server.Client(), server.URL, "small32/SClash", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if result.TagName != "v0.9.0" || len(result.Assets) != 5 || result.Assets[0].Platform != "windows" || result.Assets[0].Arch != "x64" || result.Assets[1].Arch != "arm64" || result.Assets[2].Arch != "arm64-v8a" || result.Assets[3].Arch != "x64 · 便携版" || result.Assets[4].Arch != "armeabi-v7a" || result.Assets[0].SHA256 != "abcd" {
		t.Fatalf("parsed assets: %+v", result)
	}
	_, err = fetchClientGitHubRelease(context.Background(), server.Client(), server.URL, "small32/SClash", "")
	if err == nil || !strings.Contains(err.Error(), "令牌权限") {
		t.Fatalf("missing private repo credentials: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatal("unexpected requests")
	}
}
func TestClientGitHubCacheAndDownloadAuthorization(t *testing.T) {
	clientTestDB(t)
	s := new(ClientService)
	token := "cache-secret"
	cfg := &ClientSettings{Enabled: true, GitHubReleaseEnable: true, GitHubReleaseURL: "https://github.com/small32/SClash/releases/"}
	if err := s.SaveSettings(cfg, &token); err != nil {
		t.Fatal(err)
	}
	key := "small32/sclash:" + clientHash(token)
	clientGitHubReleaseCache.Store(key, &clientGitHubCacheEntry{nextCheck: time.Now().Add(time.Hour), release: &ClientGitHubRelease{TagName: "v1", Assets: []ClientReleaseAsset{{ID: 42, Name: "SClash.exe", Platform: "windows", Size: 9}}}})
	defer clientGitHubReleaseCache.Delete(key)
	release, err := s.GitHubRelease(context.Background(), false)
	if err != nil || release.TagName != "v1" {
		t.Fatal(err)
	}
	release.Assets[0].Name = "mutated"
	next, _ := s.GitHubRelease(context.Background(), false)
	if next.Assets[0].Name != "SClash.exe" {
		t.Fatal("caller mutated shared cache")
	}
	if response, _, err := s.OpenGitHubAsset(context.Background(), 999); err == nil || response != nil {
		t.Fatal("unknown asset could be fetched")
	}
	cfg.GitHubReleaseEnable = false
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	disabled, _ := s.GitHubRelease(context.Background(), false)
	if len(disabled.Assets) != 0 {
		t.Fatal("disabled GitHub source still published")
	}
}

func TestClientGitHubPrivateAssetStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/small32/SClash/releases/assets/42" || r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("Accept") != "application/octet-stream" {
			t.Errorf("wrong private download request: %s", r.URL.Path)
			w.WriteHeader(403)
			return
		}
		if r.Header.Get("Range") != "" {
			t.Error("forwarded Range enables multisegment download")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, "installer")
	}))
	defer server.Close()
	response, err := openClientGitHubAsset(context.Background(), server.Client(), server.URL, "small32/SClash", "test-secret", 42)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "installer" {
		t.Fatalf("asset stream: %s %v", body, err)
	}
	clientTestDB(t)
	client := newClientGitHubHTTPClient(true)
	request := &http.Request{URL: &url.URL{Scheme: "https", Host: "release-assets.githubusercontent.com"}, Header: http.Header{"Authorization": []string{"Bearer test-secret"}}}
	if err := client.CheckRedirect(request, nil); err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Authorization") != "" {
		t.Fatal("PAT forwarded to asset CDN")
	}
	request.URL.Scheme = "http"
	if client.CheckRedirect(request, nil) == nil {
		t.Fatal("insecure redirect allowed")
	}
}

func TestClientGitHubAssetRedirectOmitsEmptyRangeHeaders(t *testing.T) {
	clientTestDB(t)
	for _, tc := range []struct {
		name, rangeHeader, ifRange string
		status                     int
		body                       string
	}{
		{name: "ordinary browser download", status: http.StatusOK, body: "installer"},
		{name: "range without validator", rangeHeader: "bytes=0-3", status: http.StatusPartialContent, body: "inst"},
		{name: "range with validator", rangeHeader: "bytes=0-3", ifRange: `"file-v1"`, status: http.StatusPartialContent, body: "inst"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var apiCalls, cdnCalls int32
			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// A present-but-empty Range is different from an absent Range:
				// the real release-assets CDN rejects the former with HTTP 501.
				for name, want := range map[string]string{"Range": tc.rangeHeader, "If-Range": tc.ifRange} {
					values, present := r.Header[name]
					if present && (len(values) == 0 || strings.TrimSpace(values[0]) == "") {
						t.Errorf("%s received empty %s", r.URL.Path, name)
						w.WriteHeader(http.StatusNotImplemented)
						return
					}
					if want == "" && present || want != "" && (!present || r.Header.Get(name) != want) {
						t.Errorf("%s %s: got %q (present=%v), want %q", r.URL.Path, name, r.Header.Get(name), present, want)
					}
				}
				if r.Method != http.MethodGet || r.Header.Get("Accept") != "application/octet-stream" {
					t.Errorf("unexpected asset request: %s Accept=%q", r.Method, r.Header.Get("Accept"))
				}
				switch r.URL.Path {
				case "/repos/small32/SClash/releases/assets/42":
					atomic.AddInt32(&apiCalls, 1)
					if r.Header.Get("Authorization") != "Bearer test-installation-token" {
						t.Error("private API request omitted its installation token")
					}
					http.Redirect(w, r, server.URL+"/cdn", http.StatusFound)
				case "/cdn":
					atomic.AddInt32(&cdnCalls, 1)
					if _, present := r.Header["Authorization"]; present {
						t.Error("installation token leaked to the asset CDN")
					}
					w.Header().Set("Content-Type", "application/octet-stream")
					if tc.status == http.StatusPartialContent {
						w.Header().Set("Content-Range", "bytes 0-3/9")
					}
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				default:
					t.Errorf("unexpected download path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client := newClientGitHubHTTPClient(true)
			// Trust only the mock TLS certificate while retaining production
			// redirect handling, including removing API credentials at the CDN.
			client.Transport = server.Client().Transport
			response, err := openClientGitHubAsset(context.Background(), client, server.URL, "small32/SClash", "test-installation-token", 42, tc.rangeHeader, tc.ifRange)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != tc.status || string(body) != tc.body {
				t.Fatalf("redirected asset response: status=%d body=%q error=%v", response.StatusCode, body, err)
			}
			if atomic.LoadInt32(&apiCalls) != 1 || atomic.LoadInt32(&cdnCalls) != 1 {
				t.Fatalf("download did not follow API to CDN exactly once: api=%d cdn=%d", apiCalls, cdnCalls)
			}
		})
	}
}

func TestClientGitHubDownloadModeDefaults(t *testing.T) {
	clientTestDB(t)
	s := new(ClientService)
	if relay, err := s.GitHubRelayEnabled(); err != nil || relay {
		t.Fatalf("default should use GitHub direct: %v %v", relay, err)
	}
	if err := setValue(database.GetDB(), "localProxyEnable", "true"); err != nil {
		t.Fatal(err)
	}
	if relay, err := s.GitHubRelayEnabled(); err != nil || !relay {
		t.Fatalf("local upstream proxy must default to relay: %v %v", relay, err)
	}
	if err := s.SaveSettings(&ClientSettings{Enabled: true, GitHubDownloadMode: "direct"}); err != nil {
		t.Fatal(err)
	}
	if relay, _ := s.GitHubRelayEnabled(); relay {
		t.Fatal("explicit direct ignored")
	}
	setValue(database.GetDB(), "localProxyEnable", "false")
	s.SaveSettings(&ClientSettings{Enabled: true, GitHubDownloadMode: "relay"})
	if relay, _ := s.GitHubRelayEnabled(); !relay {
		t.Fatal("explicit relay ignored")
	}
	if s.SaveSettings(&ClientSettings{GitHubDownloadMode: "invalid"}) == nil {
		t.Fatal("invalid mode accepted")
	}
}
func TestClientGitHubTemporaryDirectLink(t *testing.T) {
	var cdnFetches int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cdn" {
			atomic.AddInt32(&cdnFetches, 1)
			fmt.Fprint(w, "must not download bytes")
			return
		}
		if r.Header.Get("Authorization") != "Bearer private-secret" {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Location", "https://release-assets.githubusercontent.com/signed-installer?signature=temporary")
		w.WriteHeader(302)
	}))
	defer server.Close()
	link, err := resolveClientGitHubAssetURL(context.Background(), server.Client(), server.URL, "small32/SClash", "private-secret", 42)
	if err != nil || !strings.HasPrefix(link, "https://release-assets.githubusercontent.com/") || strings.Contains(link, "private-secret") {
		t.Fatalf("private signed link: %s %v", link, err)
	}
	if atomic.LoadInt32(&cdnFetches) != 0 {
		t.Fatal("direct link resolver fetched package bytes")
	}
	noRedirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, "installer")
	}))
	defer noRedirect.Close()
	if _, err := resolveClientGitHubAssetURL(context.Background(), noRedirect.Client(), noRedirect.URL, "small32/SClash", "private-secret", 42); err == nil {
		t.Fatal("API 200 without signed URL must direct user to optional relay")
	}
}
func TestClientGitHubRelayRangesAndFiveThreads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-3" || r.Header.Get("If-Range") != `"file-v1"` {
			t.Error("range headers not forwarded")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Range", "bytes 0-3/9")
		w.WriteHeader(206)
		fmt.Fprint(w, "inst")
	}))
	defer server.Close()
	response, err := openClientGitHubAsset(context.Background(), server.Client(), server.URL, "small32/SClash", "private-secret", 42, "bytes=0-3", `"file-v1"`)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != 206 || response.Header.Get("Content-Range") != "bytes 0-3/9" || string(data) != "inst" {
		t.Fatalf("partial response lost: %d %s", response.StatusCode, data)
	}
	var releases []func()
	for i := 0; i < 5; i++ {
		release, ok := AcquireClientRelayDownload("relay-test")
		if !ok {
			t.Fatalf("thread %d denied", i+1)
		}
		releases = append(releases, release)
	}
	if _, ok := AcquireClientRelayDownload("relay-test"); ok {
		t.Fatal("sixth simultaneous relay allowed")
	}
	other, ok := AcquireClientRelayDownload("other-relay-test")
	if !ok {
		t.Fatal("unrelated account blocked")
	}
	other()
	releases[0]()
	replacement, ok := AcquireClientRelayDownload("relay-test")
	if !ok {
		t.Fatal("finished thread slot not released")
	}
	replacement()
	for _, release := range releases {
		release()
	}
}
