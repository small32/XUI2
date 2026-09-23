package random

import (
	"crypto/rand"
	"math/big"
	"time"
)

var numSeq [10]rune
var lowerSeq [26]rune
var upperSeq [26]rune
var numLowerSeq [36]rune
var numUpperSeq [36]rune
var allSeq [62]rune

func init() {
	for i := 0; i < 10; i++ {
		numSeq[i] = rune('0' + i)
	}
	for i := 0; i < 26; i++ {
		lowerSeq[i] = rune('a' + i)
		upperSeq[i] = rune('A' + i)
	}

	copy(numLowerSeq[:], numSeq[:])
	copy(numLowerSeq[len(numSeq):], lowerSeq[:])

	copy(numUpperSeq[:], numSeq[:])
	copy(numUpperSeq[len(numSeq):], upperSeq[:])

	copy(allSeq[:], numSeq[:])
	copy(allSeq[len(numSeq):], lowerSeq[:])
	copy(allSeq[len(numSeq)+len(lowerSeq):], upperSeq[:])
}

// Seq 生成 n 位随机串，用于会话签名密钥等安全敏感场景。
// 使用 crypto/rand 而不是 math/rand：后者用 UnixNano 作种子可预测，
// 知道进程启动时间即可离线枚举伪造出相同密钥。
func Seq(n int) string {
	max := big.NewInt(int64(len(allSeq)))
	runes := make([]rune, n)
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			// crypto/rand 几乎不会失败；若失败，退化为当前时间的纳秒，
			// 保证调用方不因随机源故障而崩溃。
			idx = big.NewInt(int64(time.Now().UnixNano() % int64(len(allSeq))))
		}
		runes[i] = allSeq[idx.Int64()]
	}
	return string(runes)
}
