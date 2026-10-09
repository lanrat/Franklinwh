package franklinwh

import (
	"context"
	"errors"
	"net/url"
)

// ErrNoGateways is returned by DefaultGateway when the account has none.
var ErrNoGateways = errors.New("franklinwh: no gateways on this account")

// Gateway is one aGate device on the account, as returned by Gateways.
type Gateway struct {
	ID           string `json:"id"`      // gateway ID, used by every per-device call
	Name         string `json:"name"`    // user-assigned name
	Account      string `json:"account"` // owner's login
	Status       int    `json:"status"`
	ProtocolVer  string `json:"protocolVer"`
	Version      string `json:"version"`
	Address      string `json:"address"`
	ActiveStatus int    `json:"activeStatus"`
	ActiveTime   string `json:"activeTime"`
	CreateTime   string `json:"createTime"`
	DeviceTime   string `json:"deviceTime"`
	TimeZone     any    `json:"timeZone"`
	ZoneInfo     string `json:"zoneInfo"`
}

// Gateways lists the aGate devices visible to the logged-in homeowner
// (GET /hes-gateway/terminal/getHomeGatewayList). Most homeowners have one.
func (c *Client) Gateways(ctx context.Context) ([]Gateway, error) {
	resp, err := c.do(ctx, request{method: "GET", path: gatewayPrefix + "/terminal/getHomeGatewayList"})
	if err != nil {
		return nil, err
	}
	var out []Gateway
	if err := decodeResult(resp, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DefaultGateway returns the gateway to use when the caller has not chosen
// one: the first on the account. total is the number of gateways found, so
// callers can mention that others exist.
func (c *Client) DefaultGateway(ctx context.Context) (gw Gateway, total int, err error) {
	gws, err := c.Gateways(ctx)
	if err != nil {
		return Gateway{}, 0, err
	}
	if len(gws) == 0 {
		return Gateway{}, 0, ErrNoGateways
	}
	return gws[0], len(gws), nil
}

// DeviceCompositeInfo is the full response of getDeviceCompositeInfo: the
// device's live telemetry plus some configuration.
type DeviceCompositeInfo struct {
	CurrentWorkMode int         `json:"currentWorkMode"` // see DeviceMode
	DeviceStatus    int         `json:"deviceStatus"`    // see DeviceStatus
	RuntimeData     RuntimeData `json:"runtimeData"`
	SolarHaveVo     SolarInfo   `json:"solarHaveVo"`
	Valid           bool        `json:"valid"`
}

// RuntimeData is the live telemetry block. Power values are in kW as
// returned by the cloud. Sign conventions (confirmed against sample data):
//
//	PUti  (grid)      > 0 importing from grid,  < 0 exporting to grid
//	PFhp  (battery)   < 0 charging,             > 0 discharging
//	PSun  (solar)     >= 0 production
//	PGen  (generator) >= 0 production
//	PLoad (home)      >= 0 consumption
//
// Energy counters (kwh_*) are lifetime-style totals in kWh.
type RuntimeData struct {
	Mode            int    `json:"mode"`
	Name            string `json:"name"` // human-readable mode, e.g. "Emergency Backup"
	ElectricityType int    `json:"electricity_type"`
	RunStatus       int    `json:"run_status"` // see RunStatus
	ReportType      int    `json:"report_type"`

	// Instantaneous power, kW.
	PUti  float64 `json:"p_uti"`  // grid
	PSun  float64 `json:"p_sun"`  // solar
	PGen  float64 `json:"p_gen"`  // generator
	PFhp  float64 `json:"p_fhp"`  // battery (aPower)
	PLoad float64 `json:"p_load"` // home load

	// Battery.
	SoC      float64   `json:"soc"`      // whole-system state of charge, %
	FhpSn    []string  `json:"fhpSn"`    // per-aPower serial numbers
	FhpSoc   []float64 `json:"fhpSoc"`   // per-aPower SoC, %
	FhpPower []float64 `json:"fhpPower"` // per-aPower power, kW (<0 charging)

	// Energy counters, kWh.
	KwhUtiIn  float64 `json:"kwh_uti_in"`  // imported from grid
	KwhUtiOut float64 `json:"kwh_uti_out"` // exported to grid
	KwhSun    float64 `json:"kwh_sun"`     // solar produced
	KwhGen    float64 `json:"kwh_gen"`     // generator produced
	KwhFhpChg float64 `json:"kwh_fhp_chg"` // battery charged
	KwhFhpDi  float64 `json:"kwh_fhp_di"`  // battery discharged
	KwhLoad   float64 `json:"kwh_load"`    // home consumed

	TAmb   float64 `json:"t_amb"`  // ambient temperature
	Signal int     `json:"signal"` // cellular/wifi signal

	MainSw  []int `json:"main_sw"`  // main switch state per phase
	ProLoad []int `json:"pro_load"` // protected-load switch state

	// Grid / off-grid indicators.
	ElecnetState int `json:"elecnet_state"` // 0 = on-grid

	// Solar extras (present on some systems).
	SolarPower        int `json:"solarPower"`
	RemoteSolar1Power int `json:"remoteSolar1Power"`
	RemoteSolar2Power int `json:"remoteSolar2Power"`
	GenStat           int `json:"genStat"`
}

// SolarInfo is the solarHaveVo block.
type SolarInfo struct {
	RemoteSolarEn        int  `json:"remoteSolarEn"`
	OffGridFlag          int  `json:"offGridFlag"` // 1 = system is off-grid
	InstallProximalSolar int  `json:"installProximalsolar"`
	IsThreePhaseInstall  int  `json:"isThreePhaseInstall"`
	MpptEnFlag           bool `json:"mpptEnFlag"`
}

// GetDeviceCompositeInfo fetches the live telemetry for a gateway
// (GET /hes-gateway/terminal/getDeviceCompositeInfo). When refresh is true
// the server is asked to pull fresh data from the device (refreshFlag=1)
// rather than return its cached copy.
func (c *Client) GetDeviceCompositeInfo(ctx context.Context, gatewayID string, refresh bool) (*DeviceCompositeInfo, error) {
	flag := "0"
	if refresh {
		flag = "1"
	}
	resp, err := c.do(ctx, request{
		method: "GET",
		path:   gatewayPrefix + "/terminal/getDeviceCompositeInfo",
		query:  url.Values{"gatewayId": {gatewayID}, "refreshFlag": {flag}},
	})
	if err != nil {
		return nil, err
	}
	var out DeviceCompositeInfo
	if err := decodeResult(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Status is a simplified view of a gateway's battery, grid and solar state,
// derived from GetDeviceCompositeInfo. For the full data, call that instead.
type Status struct {
	GatewayID  string
	Mode       string // RuntimeData.Name, e.g. "Time of Use"
	Battery    BatteryStatus
	Grid       GridStatus
	Solar      SolarStatus
	Generator  GeneratorStatus
	HomeLoadKW float64 // instantaneous home consumption, kW

	// Raw is the underlying response, for callers that need more fields.
	Raw *DeviceCompositeInfo
}

// BatteryStatus summarizes the aPower battery bank.
type BatteryStatus struct {
	SoC          float64 // state of charge, %
	PowerKW      float64 // signed: <0 charging, >0 discharging
	Charging     bool
	PerUnitSoC   []float64 // per-aPower SoC, %
	PerUnitPower []float64 // per-aPower power, kW
}

// GridStatus summarizes the utility connection.
type GridStatus struct {
	PowerKW   float64 // signed: >0 importing, <0 exporting
	Importing bool
	Connected bool // best-effort: on-grid when offGridFlag==0 and elecnet_state==0
}

// SolarStatus summarizes PV production.
type SolarStatus struct {
	PowerKW   float64
	Producing bool
}

// GeneratorStatus summarizes an attached generator.
type GeneratorStatus struct {
	PowerKW float64
	Running bool
}

// Status fetches the gateway's live data and returns a simplified summary.
// It always requests a refresh so the numbers are current.
func (c *Client) Status(ctx context.Context, gatewayID string) (*Status, error) {
	info, err := c.GetDeviceCompositeInfo(ctx, gatewayID, true)
	if err != nil {
		return nil, err
	}
	rd := info.RuntimeData
	return &Status{
		GatewayID:  gatewayID,
		Mode:       rd.Name,
		HomeLoadKW: rd.PLoad,
		Battery: BatteryStatus{
			SoC:          rd.SoC,
			PowerKW:      rd.PFhp,
			Charging:     rd.PFhp < 0,
			PerUnitSoC:   rd.FhpSoc,
			PerUnitPower: rd.FhpPower,
		},
		Grid: GridStatus{
			PowerKW:   rd.PUti,
			Importing: rd.PUti > 0,
			Connected: info.SolarHaveVo.OffGridFlag == 0 && rd.ElecnetState == 0,
		},
		Solar: SolarStatus{
			PowerKW:   rd.PSun,
			Producing: rd.PSun > 0.0005, // ignore tiny idle readings
		},
		Generator: GeneratorStatus{
			PowerKW: rd.PGen,
			Running: rd.GenStat != 0 || rd.PGen > 0.0005,
		},
		Raw: info,
	}, nil
}
