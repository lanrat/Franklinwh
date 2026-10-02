package franklinwh

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestGridLimitsRoundTrip(t *testing.T) {
	var posted map[string]any
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case gatewayPrefix + "/terminal/tou/getPowerControlSetting":
			if r.URL.Query().Get("gatewayId") != "GW1" {
				t.Errorf("gatewayId = %q", r.URL.Query().Get("gatewayId"))
			}
			io.WriteString(w, `{"code":200,"success":true,"result":{"globalGridDischargeMax":2.0,"globalGridChargeMax":2.5,"gridFeedMax":10.0,"gridFeedMaxFlag":2,"gridMax":-1.0,"gridMaxFlag":2,"sgipFlag":1,"peakDemandGridMax":null}}`)
		case gatewayPrefix + "/terminal/tou/setPowerControlV2":
			if v := r.Header.Get("softwareVersion"); v != "APP"+gridLimitsAppVersion {
				t.Errorf("softwareVersion = %q", v)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &posted)
			io.WriteString(w, `{"code":200,"success":true}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	ctx := context.Background()
	l, err := c.GridLimits(ctx, "GW1")
	if err != nil {
		t.Fatal(err)
	}
	if l.ImportKW != 2.5 || l.ExportKW != 2 || l.ImportFlag != GridLimitLimited || l.ExportFlag != GridLimitLimited {
		t.Fatalf("limits = %+v", l)
	}
	l.ImportKW, l.ExportKW = 5, 0.5
	if err := c.SetGridLimits(ctx, "GW1", l); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"gatewayId": "GW1", "gridMax": 5.0, "globalGridChargeMax": 5.0,
		"gridFeedMax": 0.5, "globalGridDischargeMax": 0.5,
		"gridMaxFlag": 2.0, "gridFeedMaxFlag": 2.0,
		"sgipFlag": 1.0, "peakDemandGridMax": nil, // untouched fields pass through
	}
	for k, v := range want {
		if got, ok := posted[k]; !ok || got != v {
			t.Errorf("%s = %v (present %v), want %v", k, got, ok, v)
		}
	}
}
