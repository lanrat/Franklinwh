package franklinwh

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// sample getDeviceCompositeInfo result, trimmed from a real app response
// (asset/mock_data in the APK). Grid importing 0.655 kW, battery charging
// (p_fhp negative), solar idle, SoC ~99%.
const sampleComposite = `{
  "code": 200, "message": "Query success!", "success": true, "total": 1,
  "result": {
    "currentWorkMode": 3,
    "deviceStatus": 1,
    "runtimeData": {
      "mode": 2775, "name": "Emergency Backup", "electricity_type": 1,
      "run_status": 1, "report_type": 1,
      "fhpSn": ["10050002A02J22020005","10050002A02J22020173"],
      "p_uti": 0.655, "p_sun": -0.007, "p_gen": 0.0, "p_fhp": -0.163, "p_load": 0.485,
      "kwh_uti_in": 8.031, "kwh_uti_out": 0.0, "kwh_sun": 0.0, "kwh_fhp_chg": 0.653,
      "kwh_fhp_di": 0.0, "kwh_load": 7.378,
      "soc": 99.158, "t_amb": 17.1,
      "main_sw": [1,0,1], "pro_load": [0,0,1],
      "signal": 42, "elecnet_state": 0,
      "solarPower": 0, "genStat": 0,
      "fhpSoc": [99.1, 99.1], "fhpPower": [-0.055, -0.049]
    },
    "solarHaveVo": { "remoteSolarEn": 0, "offGridFlag": 0, "installProximalsolar": 1 },
    "currentAlarmVOList": [],
    "valid": true
  }
}`

const sampleGatewayList = `{
  "code": 200, "message": "Query success!", "success": true, "total": 1,
  "result": [
    {"id":"10050001A02F22020077","account":"demo@example.com","status":1,"name":"FHP",
     "protocolVer":"V1.11.01","version":"V10R06B20D00","zoneInfo":"America/Chicago","timeZone":-6.0}
  ]
}`

func newTestServer(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(WithBaseURL(srv.URL), WithToken("test-token"), WithClientID("fixed-id"))
	return c, srv
}

func TestStatusParsing(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gatewayPrefix+"/terminal/getDeviceCompositeInfo" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("refreshFlag"); got != "1" {
			t.Errorf("refreshFlag = %q, want 1", got)
		}
		if got := r.URL.Query().Get("gatewayId"); got != "GW1" {
			t.Errorf("gatewayId = %q, want GW1", got)
		}
		io.WriteString(w, sampleComposite)
	})

	st, err := c.Status(context.Background(), "GW1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != "Emergency Backup" {
		t.Errorf("Mode = %q", st.Mode)
	}
	if st.Battery.SoC != 99.158 {
		t.Errorf("Battery.SoC = %v, want 99.158", st.Battery.SoC)
	}
	if !st.Battery.Charging {
		t.Errorf("Battery.Charging = false, want true (p_fhp = -0.163)")
	}
	if st.Battery.PowerKW != -0.163 {
		t.Errorf("Battery.PowerKW = %v, want -0.163", st.Battery.PowerKW)
	}
	if len(st.Battery.PerUnitSoC) != 2 {
		t.Errorf("PerUnitSoC = %v, want 2 entries", st.Battery.PerUnitSoC)
	}
	if st.Grid.PowerKW != 0.655 || !st.Grid.Importing {
		t.Errorf("Grid = %+v, want importing 0.655", st.Grid)
	}
	if !st.Grid.Connected {
		t.Errorf("Grid.Connected = false, want true (offGridFlag 0, elecnet_state 0)")
	}
	if st.Solar.Producing {
		t.Errorf("Solar.Producing = true, want false (p_sun ~0)")
	}
	if st.HomeLoadKW != 0.485 {
		t.Errorf("HomeLoadKW = %v, want 0.485", st.HomeLoadKW)
	}
	if st.Raw == nil || st.Raw.RuntimeData.KwhUtiIn != 8.031 {
		t.Errorf("Raw not populated correctly")
	}
}

func TestGateways(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/terminal/getHomeGatewayList") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		io.WriteString(w, sampleGatewayList)
	})
	gws, err := c.Gateways(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(gws) != 1 {
		t.Fatalf("got %d gateways, want 1", len(gws))
	}
	if gws[0].ID != "10050001A02F22020077" || gws[0].Name != "FHP" {
		t.Errorf("gateway = %+v", gws[0])
	}
}

func TestRequestHeaders(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		for k, want := range map[string]string{
			"loginToken":      "test-token",
			"lang":            "EN_US",
			"softwareVersion": "APP" + AppVersion,
			"optSource":       "3",
		} {
			if got := r.Header.Get(k); got != want {
				t.Errorf("header %s = %q, want %q", k, got, want)
			}
		}
		if r.Header.Get("optTime") == "" {
			t.Error("optTime header missing")
		}
		io.WriteString(w, sampleGatewayList)
	})
	if _, err := c.Gateways(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUnauthorized(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"code":401,"message":"token invalid","success":false}`)
	})
	_, err := c.Gateways(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("errors.Is(err, ErrUnauthorized) = false; err = %v", err)
	}
}

func TestLoginFlow(t *testing.T) {
	var gotBody url.Values
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/appUserOrInstallerLogin") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("client-id") != "fixed-id" {
			t.Errorf("client-id = %q", r.Header.Get("client-id"))
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q, want form encoding", ct)
		}
		r.ParseForm()
		gotBody = r.PostForm
		io.WriteString(w, `{"code":200,"success":true,"result":{"token":"new-token","userId":1205,"loginName":"demo"}}`)
	})
	// start with no token
	c.SetToken("")
	res, err := c.Login(context.Background(), "demo@example.com", "pw", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Token != "new-token" {
		t.Errorf("token = %q", res.Token)
	}
	if c.Token() != "new-token" {
		t.Errorf("client token not updated: %q", c.Token())
	}
	if gotBody.Get("account") != "demo@example.com" {
		t.Errorf("account = %v", gotBody.Get("account"))
	}
	if gotBody.Has("enc") {
		t.Errorf("enc = %q, want unset", gotBody.Get("enc"))
	}
	// lowercase hex MD5 of "pw"
	if pw := gotBody.Get("password"); pw != "8fe4c11451281c094a6578e6ddbf5eed" {
		t.Errorf("password = %q, want MD5 hex", pw)
	}
}

func TestLoginMFARequired(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"code":200,"success":true,"result":{"mfaRequired":true,"mfaToken":"mt","mfaMethod":"EMAIL_OTP","availableMfaMethods":["EMAIL_OTP"],"maskedEmail":"d***@e.com"}}`)
	})
	c.SetToken("")
	res, err := c.Login(context.Background(), "demo@example.com", "pw", nil)
	if !errors.Is(err, ErrMFARequired) {
		t.Fatalf("err = %v, want ErrMFARequired", err)
	}
	if res.MFAToken != "mt" || res.MFAMethod != "EMAIL_OTP" {
		t.Errorf("mfa result = %+v", res)
	}
	if c.Token() != "" {
		t.Errorf("token should not be set when MFA required")
	}
}

func TestDoArbitraryEndpoint(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gatewayPrefix+"/terminal/selectOffgrid" {
			t.Errorf("path = %q", r.URL.Path)
		}
		io.WriteString(w, `{"code":200,"success":true,"result":{"offgridSet":0,"offgridState":0}}`)
	})
	resp, err := c.Do(context.Background(), "GET", gatewayPrefix+"/terminal/selectOffgrid",
		map[string][]string{"gatewayId": {"GW1"}, "type": {"0"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		OffgridState int `json:"offgridState"`
	}
	if err := decodeResult(resp, &out); err != nil {
		t.Fatal(err)
	}
	if out.OffgridState != 0 {
		t.Errorf("offgridState = %d", out.OffgridState)
	}
}
