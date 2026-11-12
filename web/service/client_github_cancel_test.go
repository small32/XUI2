package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Redirect every production GitHub connection to a local TLS fixture. Keep the
// production HTTP client and URL so these tests exercise GitHubRelease itself.
func clientGitHubReleaseTestServer(t *testing.T, handler http.Handler) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, network, server.Listener.Addr().String())
	}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = previous
		transport.CloseIdleConnections()
	})
}

func clientGitHubReleaseTestCache(t *testing.T, warm bool) (*ClientService, *clientGitHubCacheEntry) {
	t.Helper()
	clientTestDB(t)
	s := new(ClientService)
	token := "release-cache-cancellation-test"
	settings := &ClientSettings{Enabled: true, GitHubReleaseEnable: true, GitHubReleaseURL: "https://github.com/small32/SClash/releases/"}
	if err := s.SaveSettings(settings, &token); err != nil {
		t.Fatal(err)
	}
	entry := &clientGitHubCacheEntry{}
	if warm {
		entry.release = &ClientGitHubRelease{TagName: "v1", FetchedAt: 123, Assets: []ClientReleaseAsset{{ID: 1, Name: "cached.exe", Platform: "windows", Size: 9}}}
		entry.nextCheck = time.Now().Add(-time.Minute)
	}
	key := "small32/sclash:" + clientHash(token)
	clientGitHubReleaseCache.Store(key, entry)
	t.Cleanup(func() { clientGitHubReleaseCache.Delete(key) })
	return s, entry
}

func TestClientGitHubReleaseCancellationDoesNotCacheFailure(t *testing.T) {
	for _, warm := range []bool{false, true} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("warm=%v/deadline=%v", warm, deadline), func(t *testing.T) {
				s, entry := clientGitHubReleaseTestCache(t, warm)
				previousRelease, previousError, previousNextCheck := entry.release, entry.err, entry.nextCheck
				started := make(chan struct{})
				var calls int32
				clientGitHubReleaseTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Host != "" || r.Host != "api.github.com" || r.URL.Path != "/repos/small32/SClash/releases/latest" {
						t.Errorf("unexpected release request: %s host=%s", r.URL, r.Host)
					}
					if atomic.AddInt32(&calls, 1) == 1 {
						if warm {
							// Cancellation while decoding also loses its sentinel in
							// fetchClientGitHubRelease's public error message.
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusOK)
							w.(http.Flusher).Flush()
						}
						close(started)
						<-r.Context().Done()
						return
					}
					fmt.Fprint(w, `{"tag_name":"v2","assets":[{"id":2,"name":"client.exe","size":10}]}`)
				}))

				ctx, cancel := context.WithCancel(context.Background())
				wantErr := context.Canceled
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
					wantErr = context.DeadlineExceeded
				}
				defer cancel()
				type releaseResult struct {
					release *ClientGitHubRelease
					err     error
				}
				finished := make(chan releaseResult, 1)
				go func() {
					release, err := s.GitHubRelease(ctx, warm)
					finished <- releaseResult{release, err}
				}()
				select {
				case <-started:
				case result := <-finished:
					t.Fatalf("request ended before reaching local GitHub fixture: %v", result.err)
				case <-time.After(5 * time.Second):
					t.Fatal("release request did not reach local GitHub fixture")
				}
				if !deadline {
					cancel()
				}
				select {
				case result := <-finished:
					if result.release != nil || !errors.Is(result.err, wantErr) {
						t.Fatalf("canceled request returned release=%+v error=%v, want %v", result.release, result.err, wantErr)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("release request did not stop after cancellation")
				}
				if entry.release != previousRelease || entry.err != previousError || !entry.nextCheck.Equal(previousNextCheck) {
					t.Fatalf("cancellation changed shared cache: %+v", entry)
				}
				release, err := s.GitHubRelease(context.Background(), false)
				if err != nil || release == nil || release.TagName != "v2" || release.Error != "" {
					t.Fatalf("next caller could not immediately refresh: release=%+v error=%v", release, err)
				}
				if atomic.LoadInt32(&calls) != 2 {
					t.Fatalf("next caller did not fetch GitHub: calls=%d", calls)
				}
			})
		}
	}
}

func TestClientGitHubReleaseCanceledCallerDoesNotReceiveCachedRelease(t *testing.T) {
	s, entry := clientGitHubReleaseTestCache(t, true)
	entry.nextCheck = time.Now().Add(time.Hour)
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		wantErr := context.Canceled
		if deadline {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			wantErr = context.DeadlineExceeded
		}
		cancel()
		release, err := s.GitHubRelease(ctx, false)
		if release != nil || !errors.Is(err, wantErr) {
			t.Fatalf("canceled cache hit returned release=%+v error=%v, want %v", release, err, wantErr)
		}
	}
}

func TestClientGitHubReleaseFailureStillUsesNegativeCache(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(fmt.Sprintf("warm=%v", warm), func(t *testing.T) {
			s, entry := clientGitHubReleaseTestCache(t, warm)
			previousRelease := entry.release
			var calls int32
			clientGitHubReleaseTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				w.WriteHeader(http.StatusBadGateway)
			}))
			before := time.Now()
			for i := 0; i < 2; i++ {
				release, err := s.GitHubRelease(context.Background(), false)
				if warm {
					if err != nil || release == nil || release.TagName != "v1" || release.FetchedAt != 123 || release.Error == "" || release.Assets[0].Name != "cached.exe" {
						t.Fatalf("failure lost previous release: release=%+v error=%v", release, err)
					}
				} else if release != nil || err == nil {
					t.Fatalf("cold failure returned release=%+v error=%v", release, err)
				}
			}
			if entry.err == nil || entry.release != previousRelease || entry.nextCheck.Before(before.Add(time.Minute)) || entry.nextCheck.After(time.Now().Add(time.Minute)) || atomic.LoadInt32(&calls) != 1 {
				t.Fatalf("GitHub failure did not keep one-minute negative cache: entry=%+v calls=%d", entry, calls)
			}
		})
	}
}
