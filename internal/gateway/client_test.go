package gateway

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNormalizeLiveData(t *testing.T) {
	const rawJSON = `{
		"meters": {
			"last_update": 1710000000,
			"soc": 72,
			"enc_agg_soc": 73,
			"pv": {"agg_p_mw": 6200000},
			"storage": {"agg_p_mw": 3100000},
			"grid": {"agg_p_mw": -900000},
			"load": {"agg_p_mw": 8400000},
			"counters": {"sc_SendDemandRspCtrl": 2}
		}
	}`
	var raw RawLiveData
	if err := json.Unmarshal([]byte(rawJSON), &raw); err != nil {
		t.Fatal(err)
	}

	sample, err := Normalize(raw, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}

	if got, want := sample.At.Unix(), int64(1710000000); got != want {
		t.Fatalf("At=%d want %d", got, want)
	}
	if got, want := sample.SOC, 73.0; got != want {
		t.Fatalf("SOC=%v want %v", got, want)
	}
	if got, want := sample.BatteryPowerW, 3100.0; got != want {
		t.Fatalf("BatteryPowerW=%v want %v", got, want)
	}
	if got, want := sample.GridPowerW, -900.0; got != want {
		t.Fatalf("GridPowerW=%v want %v", got, want)
	}
	if sample.SendDemandRspCtrl == nil || *sample.SendDemandRspCtrl != 2 {
		t.Fatalf("SendDemandRspCtrl=%v want 2", sample.SendDemandRspCtrl)
	}
}

func TestNormalizeFallsBackToSOC(t *testing.T) {
	raw := RawLiveData{
		"meters": map[string]any{
			"soc": float64(66),
		},
	}
	sample, err := Normalize(raw, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if sample.SOC != 66 {
		t.Fatalf("SOC=%v want 66", sample.SOC)
	}
}
