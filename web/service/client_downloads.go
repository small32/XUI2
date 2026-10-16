package service

import "sync"

var clientDownloadMu sync.Mutex
var clientDownloads = map[string]int{}

func AcquireClientRelayDownload(account string) (func(), bool) {
	clientDownloadMu.Lock()
	defer clientDownloadMu.Unlock()
	if clientDownloads[account] >= 5 {
		return nil, false
	}
	clientDownloads[account]++
	var once sync.Once
	return func() {
		once.Do(func() {
			clientDownloadMu.Lock()
			defer clientDownloadMu.Unlock()
			clientDownloads[account]--
			if clientDownloads[account] <= 0 {
				delete(clientDownloads, account)
			}
		})
	}, true
}
