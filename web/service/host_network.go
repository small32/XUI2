package service

import (
	"encoding/json"
	"fmt"
	stdnet "net"
	"sync"
	"time"
	"xui/database"
	"xui/database/model"
	"xui/util/common"

	"github.com/shirou/gopsutil/host"
	"github.com/shirou/gopsutil/net"
	"gorm.io/gorm"
)

type NetworkCounter struct {
	Sent uint64 `json:"sent"`
	Recv uint64 `json:"recv"`
}
type HostMonthTraffic struct {
	Month         string `json:"month"`
	Sent          uint64 `json:"sent"`
	Recv          uint64 `json:"recv"`
	ObservedSince int64  `json:"observedSince"`
	Ready         bool   `json:"ready"`
}

var hostNetworkMu sync.Mutex

func collectNetworkCounters() (map[string]NetworkCounter, error) {
	stats, err := net.IOCounters(true)
	if err != nil {
		return nil, err
	}
	interfaces, err := stdnet.Interfaces()
	if err != nil {
		return nil, err
	}
	loopback := map[string]bool{}
	for _, iface := range interfaces {
		if iface.Flags&stdnet.FlagLoopback != 0 {
			loopback[iface.Name] = true
		}
	}
	counters := map[string]NetworkCounter{}
	for _, stat := range stats {
		if !loopback[stat.Name] {
			counters[stat.Name] = NetworkCounter{stat.BytesSent, stat.BytesRecv}
		}
	}
	if len(counters) == 0 {
		return nil, fmt.Errorf("没有可采集的非回环网卡")
	}
	return counters, nil
}

func networkDelta(current, previous map[string]NetworkCounter) (sent, recv uint64) {
	for name, curr := range current {
		old, ok := previous[name]
		if !ok {
			continue
		}
		if curr.Sent >= old.Sent {
			sent += curr.Sent - old.Sent
		} else {
			sent += curr.Sent
		}
		if curr.Recv >= old.Recv {
			recv += curr.Recv - old.Recv
		} else {
			recv += curr.Recv
		}
	}
	return
}

// Each month maintains its own baseline, so pre-installation and prior-month counters are never guessed.
func recordHostNetwork(now time.Time, boot uint64, counters map[string]NetworkCounter) error {
	hostNetworkMu.Lock()
	defer hostNetworkMu.Unlock()
	month := now.In(common.ShanghaiLocation).Format("200601")
	data, err := json.Marshal(counters)
	if err != nil {
		return err
	}
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var row model.HostNetworkMonth
		err := tx.First(&row, "month = ?", month).Error
		if database.IsNotFound(err) {
			return tx.Create(&model.HostNetworkMonth{Month: month, ObservedSince: now.UnixMilli(), SampledAt: now.UnixMilli(), BootTime: boot, Counters: string(data)}).Error
		}
		if err != nil {
			return err
		}
		if now.UnixMilli() <= row.SampledAt {
			return nil
		}
		var up, down uint64
		if row.BootTime == boot {
			var previous map[string]NetworkCounter
			if err := json.Unmarshal([]byte(row.Counters), &previous); err != nil {
				return err
			}
			up, down = networkDelta(counters, previous)
		} else {
			// A reboot resets counters; only the traffic accumulated during this new boot is added.
			monthStart := time.Date(now.In(common.ShanghaiLocation).Year(), now.In(common.ShanghaiLocation).Month(), 1, 0, 0, 0, 0, common.ShanghaiLocation)
			if int64(boot) >= monthStart.Unix() {
				for _, c := range counters {
					up += c.Sent
					down += c.Recv
				}
			}
		}
		row.Sent += up
		row.Recv += down
		row.SampledAt = now.UnixMilli()
		row.BootTime = boot
		row.Counters = string(data)
		return tx.Save(&row).Error
	})
}

func SampleHostNetwork() error {
	counters, err := collectNetworkCounters()
	if err != nil {
		return err
	}
	boot, err := host.BootTime()
	if err != nil {
		return err
	}
	return recordHostNetwork(time.Now(), boot, counters)
}

func readHostMonthTraffic(now time.Time) (HostMonthTraffic, error) {
	month := now.In(common.ShanghaiLocation).Format("200601")
	result := HostMonthTraffic{Month: now.In(common.ShanghaiLocation).Format("2006-01")}
	var row model.HostNetworkMonth
	if err := database.GetDB().First(&row, "month = ?", month).Error; err != nil {
		if database.IsNotFound(err) {
			return result, nil
		}
		return result, err
	}
	result.Sent = row.Sent
	result.Recv = row.Recv
	result.ObservedSince = row.ObservedSince
	result.Ready = true
	return result, nil
}
