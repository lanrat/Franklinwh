package franklinwh

import (
	"context"
	"encoding/json"
	"errors"
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

func TestGridLimitsApply(t *testing.T) {
	l := &GridLimits{ImportKW: 1, ImportFlag: 0, ExportKW: 2, ExportFlag: 0}
	imp := 3.5
	if err := l.Apply(&imp, nil); err != nil {
		t.Fatal(err)
	}
	if l.ImportKW != 3.5 || l.ImportFlag != GridLimitLimited || l.ExportKW != 2 || l.ExportFlag != 0 {
		t.Errorf("after Apply = %+v", l)
	}
	neg := -1.0
	if err := l.Apply(&imp, &neg); err == nil {
		t.Error("Apply accepted a negative export limit")
	}
	if l.ExportKW != 2 {
		t.Errorf("failed Apply modified limits: %+v", l)
	}
}

// gridServer serves getPowerControlSetting from *stored and, on
// setPowerControlV2, stores the posted gridMax/gridFeedMax unless frozen.
func gridServer(t *testing.T, frozen bool) (*Client, *int) {
	t.Helper()
	imp, exp := 2.5, 2.0
	sets := 0
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case gatewayPrefix + "/terminal/tou/getPowerControlSetting":
			b, _ := json.Marshal(map[string]any{"code": 200, "success": true, "result": map[string]any{
				"globalGridChargeMax": imp, "globalGridDischargeMax": exp, "gridMaxFlag": 2, "gridFeedMaxFlag": 2,
			}})
			w.Write(b)
		case gatewayPrefix + "/terminal/tou/setPowerControlV2":
			sets++
			var body struct{ GridMax, GridFeedMax float64 }
			json.NewDecoder(r.Body).Decode(&body)
			if !frozen {
				imp, exp = body.GridMax, body.GridFeedMax
			}
			io.WriteString(w, `{"code":200,"success":true}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	return c, &sets
}

func TestUpdateGridLimits(t *testing.T) {
	c, sets := gridServer(t, false)
	exp := 0.5
	after, err := c.UpdateGridLimits(context.Background(), "GW1", nil, &exp)
	if err != nil {
		t.Fatal(err)
	}
	if after.ImportKW != 2.5 || after.ExportKW != 0.5 || *sets != 1 {
		t.Errorf("after = %+v, sets = %d", after, *sets)
	}

	neg := -2.0
	if _, err := c.UpdateGridLimits(context.Background(), "GW1", &neg, nil); err == nil {
		t.Error("negative import limit accepted")
	}
	if *sets != 1 {
		t.Errorf("negative limit was sent to the server")
	}
}

func TestUpdateGridLimitsNotApplied(t *testing.T) {
	c, _ := gridServer(t, true)
	imp := 7.0
	after, err := c.UpdateGridLimits(context.Background(), "GW1", &imp, nil)
	if !errors.Is(err, ErrGridLimitsNotApplied) {
		t.Fatalf("err = %v, want ErrGridLimitsNotApplied", err)
	}
	if after == nil || after.ImportKW != 2.5 {
		t.Errorf("after = %+v, want the unchanged limits", after)
	}
}
