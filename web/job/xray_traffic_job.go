package job

import (
	"sync"

	"x-ui/logger"
	"x-ui/web/service"
	"x-ui/xray"
)

type XrayTrafficJob struct {
	xrayService    service.XrayService
	inboundService service.InboundService

	// baseline 记录每个入站上一次已成功落库的累计 up/down。
	// GetXrayTraffic 现在读累计值且不再重置 xray 计数器，
	// 本 job 通过"当前累计 - 基线"得到增量，只有写库成功才推进基线。
	// 这样一旦写库失败，下一轮还能用新的累计减旧基线把缺口补回来，流量不丢失。
	mu            sync.Mutex
	baseline      map[string]xray.Traffic
	generation    uint64
	hasGeneration bool
}

func NewXrayTrafficJob() *XrayTrafficJob {
	return &XrayTrafficJob{
		baseline: make(map[string]xray.Traffic),
	}
}

func (j *XrayTrafficJob) Run() {
	if !j.xrayService.IsXrayRunning() {
		j.mu.Lock()
		// A stopped/restarted Xray has a fresh stats counter. Do not subtract
		// the new process counters from the previous process baseline.
		j.baseline = make(map[string]xray.Traffic)
		j.hasGeneration = false
		j.mu.Unlock()
		return
	}
	cur, generation, err := j.xrayService.GetXrayTrafficSnapshot()
	if err != nil {
		logger.Warning("get xray traffic failed:", err)
		return
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	deltas, newBaseline := trafficDeltasForGeneration(generation, j.generation, j.hasGeneration, cur, j.baseline)
	if !j.hasGeneration || generation != j.generation {
		j.baseline = make(map[string]xray.Traffic)
		j.generation = generation
		j.hasGeneration = true
	}
	if len(deltas) == 0 {
		j.baseline = newBaseline
		return
	}
	// Only advance the baseline after all increments have committed. A failed
	// write is retried from the old baseline on the next poll.
	if err := j.inboundService.AddTraffic(deltas); err != nil {
		logger.Warning("add traffic failed, will retry next round:", err)
		return
	}
	j.baseline = newBaseline
}

func trafficDeltasForGeneration(generation, baselineGeneration uint64, hasGeneration bool, cur []*xray.Traffic, baseline map[string]xray.Traffic) ([]*xray.Traffic, map[string]xray.Traffic) {
	if !hasGeneration || generation != baselineGeneration {
		baseline = make(map[string]xray.Traffic)
	}
	return trafficDeltas(cur, baseline)
}

func trafficDeltas(cur []*xray.Traffic, baseline map[string]xray.Traffic) ([]*xray.Traffic, map[string]xray.Traffic) {
	deltas := make([]*xray.Traffic, 0)
	newBaseline := make(map[string]xray.Traffic, len(cur))
	for _, item := range cur {
		if !item.IsInbound {
			continue
		}
		newBaseline[item.Tag] = *item
		last, ok := baseline[item.Tag]
		deltaUp, deltaDown := item.Up, item.Down
		if ok {
			deltaUp = item.Up - last.Up
			deltaDown = item.Down - last.Down
			if deltaUp < 0 || deltaDown < 0 {
				// Counters reset within one generation (for example an operator
				// reset them externally); count the new counter values from zero.
				deltaUp, deltaDown = item.Up, item.Down
			}
		}
		if deltaUp > 0 || deltaDown > 0 {
			deltas = append(deltas, &xray.Traffic{IsInbound: true, Tag: item.Tag, Up: deltaUp, Down: deltaDown})
		}
	}
	return deltas, newBaseline
}
