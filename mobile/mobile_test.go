package mobile

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lanrat/franklinwh/mobile/schema"
)

const gatewayList = `{"code":200,"success":true,"result":[{"id":"GW1","name":"Home","status":1,"version":"V10","zoneInfo":"America/Chicago","timeZone":-6.0}]}`

const composite = `{"code":200,"success":true,"result":{
  "runtimeData":{"name":"Self Consumption","p_uti":-1.5,"p_sun":4.2,"p_gen":0,"p_fhp":-1.0,"p_load":1.7,
    "soc":80.5,"fhpSn":["A1","A2"],"fhpSoc":[80,81],"fhpPower":[-0.5],
    "kwh_uti_in":3,"kwh_uti_out":9,"kwh_sun":20,"kwh_fhp_chg":5,"kwh_fhp_di":1,"kwh_load":12,"t_amb":21.5,
    "elecnet_state":0,"unknownField":true},
  "solarHaveVo":{"offGridFlag":0}}}`

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewClient(&Config{BaseURL: srv.URL, Token: "tok", ClientID: "cid"})
}

func decode[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decoding %s: %v", s, err)
	}
	return v
}

func TestNewClientConfig(t *testing.T) {
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header
		io.WriteString(w, gatewayList)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(&Config{BaseURL: srv.URL, ClientID: "cid", DeviceModel: "Pixel 9", OSVersion: "16"})
	if c.Token() != "" || c.ClientID() != "cid" {
		t.Errorf("Token = %q, ClientID = %q", c.Token(), c.ClientID())
	}
	c.SetToken("tok")
	if _, err := c.Gateways(); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"loginToken": "tok", "optDevice": "Pixel 9", "optDeviceName": "franklinwh-go", "optSystemVersion": "Android 16",
	} {
		if got := hdr.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
	if id := NewClient(nil).ClientID(); id == "" {
		t.Error("NewClient(nil) has no client ID")
	}
}

func TestLogin(t *testing.T) {
	mfa := false
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/appUserOrInstallerLogin"):
			r.ParseForm()
			if r.PostForm.Get("timezone") != "America/Chicago" {
				t.Errorf("timezone = %q", r.PostForm.Get("timezone"))
			}
			if mfa {
				io.WriteString(w, `{"code":200,"success":true,"result":{"mfaRequired":true,"mfaToken":"mt","availableMfaMethods":["EMAIL_OTP","TOTP"],"maskedEmail":"d***@e.com"}}`)
				return
			}
			io.WriteString(w, `{"code":200,"success":true,"result":{"token":"new"}}`)
		case strings.HasSuffix(r.URL.Path, "/mfa/email-otp/login/send"):
			io.WriteString(w, `{"code":200,"success":true}`)
		case strings.HasSuffix(r.URL.Path, "/mfa/login/verify"):
			b, _ := io.ReadAll(r.Body)
			body := decode[map[string]any](t, string(b))
			if body["code"] != "123456" || body["deviceFingerprint"] != "cid" || body["rememberDevice"] != true {
				t.Errorf("verify body = %v", body)
			}
			io.WriteString(w, `{"code":200,"success":true,"result":{"token":"mfa-tok"}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	c.SetToken("")

	res, err := c.Login("a@b.c", "pw", "America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	if lr := decode[schema.LoginResult](t, res); lr.MFARequired || c.Token() != "new" {
		t.Errorf("login = %s, token %q", res, c.Token())
	}

	mfa = true
	c.SetToken("")
	res, err = c.Login("a@b.c", "pw", "America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	lr := decode[schema.LoginResult](t, res)
	if !lr.MFARequired || lr.MFAToken != "mt" || lr.Method != "EMAIL_OTP" || len(lr.Methods) != 2 || lr.MaskedEmail != "d***@e.com" {
		t.Errorf("mfa login = %s", res)
	}
	if err := c.SendEmailOTP(lr.MFAToken); err != nil {
		t.Fatal(err)
	}
	if err := c.VerifyMFA(lr.MFAToken, lr.Method, "  ", true); ErrorKind(errString(err)) != ErrKindInvalid {
		t.Errorf("blank code: err = %v", err)
	}
	if err := c.VerifyMFA(lr.MFAToken, lr.Method, " 123456\n", true); err != nil {
		t.Fatal(err)
	}
	if c.Token() != "mfa-tok" {
		t.Errorf("token = %q", c.Token())
	}

	if _, err := c.Login("", "pw", ""); ErrorKind(errString(err)) != ErrKindInvalid {
		t.Errorf("empty email: err = %v", err)
	}
}

func TestStatus(t *testing.T) {
	now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getHomeGatewayList"):
			io.WriteString(w, gatewayList)
		case strings.HasSuffix(r.URL.Path, "/getDeviceCompositeInfo"):
			if r.URL.Query().Get("gatewayId") != "GW1" {
				t.Errorf("gatewayId = %q", r.URL.Query().Get("gatewayId"))
			}
			io.WriteString(w, composite)
		}
	})
	s, err := c.Status("") // resolves the default gateway
	if err != nil {
		t.Fatal(err)
	}
	st := decode[schema.Status](t, s)
	if st.GatewayID != "GW1" || st.Mode != "Self Consumption" || st.HomeLoadKW != 1.7 || st.AmbientTemp != 21.5 {
		t.Errorf("status = %s", s)
	}
	if b := st.Battery; b.SoC != 80.5 || !b.Charging || len(b.Units) != 2 ||
		b.Units[1] != (schema.BatteryUnit{Serial: "A2", SoC: 81, PowerKW: 0}) {
		t.Errorf("battery = %+v", b)
	}
	if st.Grid != (schema.GridStatus{PowerKW: -1.5, Importing: false, Connected: true}) {
		t.Errorf("grid = %+v", st.Grid)
	}
	if !st.Solar.Producing || st.Energy.GridExportKWh != 9 || st.Energy.HomeKWh != 12 {
		t.Errorf("solar = %+v, energy = %+v", st.Solar, st.Energy)
	}
	if st.UpdatedAt != "2026-01-02T03:04:05Z" {
		t.Errorf("updatedAt = %q", st.UpdatedAt)
	}
	// The keys the app relies on.
	for _, k := range []string{`"gatewayId"`, `"homeLoadKW"`, `"powerKW"`, `"units"`, `"connected"`, `"gridImportKWh"`} {
		if !strings.Contains(s, k) {
			t.Errorf("status JSON lacks %s: %s", k, s)
		}
	}

	raw, err := c.RawStatus("GW1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"unknownField":true`) {
		t.Errorf("RawStatus dropped fields: %s", raw)
	}
}

func TestGridLimits(t *testing.T) {
	imp, exp := 2.5, 2.0
	impFlag, expFlag := 2, 1
	frozen := false
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getPowerControlSetting"):
			b, _ := json.Marshal(map[string]any{"code": 200, "success": true, "result": map[string]any{
				"globalGridChargeMax": imp, "globalGridDischargeMax": exp, "gridMaxFlag": impFlag, "gridFeedMaxFlag": expFlag,
			}})
			w.Write(b)
		case strings.HasSuffix(r.URL.Path, "/setPowerControlV2"):
			var body struct {
				GridMax, GridFeedMax         float64
				GridMaxFlag, GridFeedMaxFlag int
			}
			json.NewDecoder(r.Body).Decode(&body)
			if !frozen {
				imp, exp = body.GridMax, body.GridFeedMax
				impFlag, expFlag = body.GridMaxFlag, body.GridFeedMaxFlag
			}
			io.WriteString(w, `{"code":200,"success":true}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})

	s, err := c.GridLimits("GW1")
	if err != nil {
		t.Fatal(err)
	}
	if g := decode[schema.GridLimits](t, s); g.ImportKW != 2.5 || !g.ImportLimited || g.ExportLimited || g.Applied != nil {
		t.Errorf("limits = %s", s)
	}
	if strings.Contains(s, "applied") {
		t.Errorf("GridLimits should omit applied: %s", s)
	}

	s, err = c.UpdateGridLimits("GW1", math.NaN(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if g := decode[schema.GridLimits](t, s); g.ImportKW != 2.5 || g.ExportKW != 1 || !g.ExportLimited || g.Applied == nil || !*g.Applied {
		t.Errorf("updated = %s", s)
	}

	frozen = true
	s, err = c.UpdateGridLimits("GW1", 9, math.NaN())
	if err != nil {
		t.Fatal(err)
	}
	if g := decode[schema.GridLimits](t, s); g.ImportKW != 2.5 || g.Applied == nil || *g.Applied {
		t.Errorf("not-applied update = %s", s)
	}

	for _, v := range [][2]float64{{-1, math.NaN()}, {math.NaN(), math.Inf(1)}, {math.NaN(), math.NaN()}} {
		if _, err := c.UpdateGridLimits("GW1", v[0], v[1]); ErrorKind(errString(err)) != ErrKindInvalid {
			t.Errorf("UpdateGridLimits(%v): err = %v, want invalid", v, err)
		}
	}
}

func TestErrorKinds(t *testing.T) {
	var status atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch status.Load() {
		case 401:
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"code":401,"message":"token invalid"}`)
		case 500:
			io.WriteString(w, `{"code":500,"message":"boom","success":false}`)
		default:
			io.WriteString(w, `{"code":200,"success":true,"result":[]}`)
		}
	})
	for code, want := range map[int32]string{401: ErrKindUnauthorized, 500: ErrKindAPI, 200: ErrKindNoGateways} {
		status.Store(code)
		_, err := c.Status("")
		if got := ErrorKind(errString(err)); got != want {
			t.Errorf("status %d: kind %q (err %v), want %q", code, got, err, want)
		}
	}

	down := NewClient(&Config{BaseURL: "http://127.0.0.1:1", Token: "t"})
	if _, err := down.Gateways(); ErrorKind(errString(err)) != ErrKindNetwork {
		t.Errorf("unreachable server: err = %v, want network", err)
	}

	for msg, want := range map[string]string{
		"unauthorized: x": ErrKindUnauthorized, "api: a: b": ErrKindAPI, "bogus: x": ErrKindOther, "": ErrKindOther,
	} {
		if got := ErrorKind(msg); got != want {
			t.Errorf("ErrorKind(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestCancel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("block") == "" {
			io.WriteString(w, gatewayList)
			return
		}
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	errc := make(chan error, 1)
	go func() {
		ctx, cancel := c.ctx()
		defer cancel()
		_, err := c.c.Do(ctx, "GET", "/x", map[string][]string{"block": {"1"}}, nil)
		errc <- wrap(err)
	}()
	<-started
	c.Cancel()
	if err := <-errc; ErrorKind(errString(err)) != ErrKindCanceled {
		t.Errorf("canceled call: err = %v", err)
	}
	if _, err := c.Gateways(); err != nil {
		t.Errorf("call after Cancel: %v", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
