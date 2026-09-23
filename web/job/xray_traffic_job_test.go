package job

import (
	"testing"
	"x-ui/xray"
)

func TestTrafficDeltasCountFirstSampleAndCounterReset(t *testing.T) {
	first := []*xray.Traffic{{IsInbound: true, Tag: "inbound-1", Up: 100, Down: 40}}
	deltas, baseline := trafficDeltas(first, map[string]xray.Traffic{})
	if len(deltas) != 1 || deltas[0].Up != 100 || deltas[0].Down != 40 {
		t.Fatalf("first sample was not counted from process start: %+v", deltas)
	}
	if got := baseline["inbound-1"]; got.Up != 100 || got.Down != 40 {
		t.Fatalf("unexpected first baseline: %+v", got)
	}

	next := []*xray.Traffic{{IsInbound: true, Tag: "inbound-1", Up: 130, Down: 55}}
	deltas, baseline = trafficDeltas(next, baseline)
	if len(deltas) != 1 || deltas[0].Up != 30 || deltas[0].Down != 15 {
		t.Fatalf("increment is wrong: %+v", deltas)
	}

	// A new process can already have more traffic than the previous process's
	// last sample. Generation identity must reset the baseline before subtracting.
	restarted := []*xray.Traffic{{IsInbound: true, Tag: "inbound-1", Up: 200, Down: 80}}
	deltas, _ = trafficDeltasForGeneration(2, 1, true, restarted, baseline)
	if len(deltas) != 1 || deltas[0].Up != 200 || deltas[0].Down != 80 {
		t.Fatalf("new process counters were subtracted from old process: %+v", deltas)
	}

	// Counters can also reset within one process if an operator clears stats.
	reset := []*xray.Traffic{{IsInbound: true, Tag: "inbound-1", Up: 20, Down: 8}}
	deltas, _ = trafficDeltasForGeneration(1, 1, true, reset, baseline)
	if len(deltas) != 1 || deltas[0].Up != 20 || deltas[0].Down != 8 {
		t.Fatalf("counter reset traffic was dropped: %+v", deltas)
	}
}
