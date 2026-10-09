// Package schema defines the JSON documents returned by package mobile.
// The Android app mirrors these types as Kotlin data classes; keep the two
// in sync. They live in their own package so that gomobile does not try to
// bind them to Java.
package schema

// LoginResult is returned by Client.Login.
type LoginResult struct {
	// MFARequired is true when a second factor is needed: call SendEmailOTP
	// (for EMAIL_OTP) and VerifyMFA with MFAToken.
	MFARequired bool     `json:"mfaRequired"`
	MFAToken    string   `json:"mfaToken,omitempty"`
	Method      string   `json:"method,omitempty"`  // method to offer first
	Methods     []string `json:"methods,omitempty"` // all available methods
	MaskedEmail string   `json:"maskedEmail,omitempty"`
}

// Gateway is one element of the array returned by Client.Gateways.
type Gateway struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       int    `json:"status"`
	ActiveStatus int    `json:"activeStatus"`
	Version      string `json:"version"`
	ProtocolVer  string `json:"protocolVer"`
	ZoneInfo     string `json:"zoneInfo"`
	Address      string `json:"address"`
}

// Status is returned by Client.Status. Power is in kW, energy in kWh.
type Status struct {
	GatewayID   string          `json:"gatewayId"`
	Mode        string          `json:"mode"`
	HomeLoadKW  float64         `json:"homeLoadKW"`
	Battery     BatteryStatus   `json:"battery"`
	Grid        GridStatus      `json:"grid"`
	Solar       SolarStatus     `json:"solar"`
	Generator   GeneratorStatus `json:"generator"`
	Energy      EnergyTotals    `json:"energy"`
	AmbientTemp float64         `json:"ambientTemp"`
	// UpdatedAt is when the data was fetched (RFC 3339, phone clock).
	UpdatedAt string `json:"updatedAt"`
}

// BatteryStatus is the battery part of Status.
type BatteryStatus struct {
	SoC      float64       `json:"soc"`     // %
	PowerKW  float64       `json:"powerKW"` // <0 charging, >0 discharging
	Charging bool          `json:"charging"`
	Units    []BatteryUnit `json:"units"`
}

// BatteryUnit is one aPower.
type BatteryUnit struct {
	Serial  string  `json:"serial"`
	SoC     float64 `json:"soc"`
	PowerKW float64 `json:"powerKW"`
}

// GridStatus is the grid part of Status.
type GridStatus struct {
	PowerKW   float64 `json:"powerKW"` // >0 importing, <0 exporting
	Importing bool    `json:"importing"`
	Connected bool    `json:"connected"`
}

// SolarStatus is the solar part of Status.
type SolarStatus struct {
	PowerKW   float64 `json:"powerKW"`
	Producing bool    `json:"producing"`
}

// GeneratorStatus is the generator part of Status.
type GeneratorStatus struct {
	PowerKW float64 `json:"powerKW"`
	Running bool    `json:"running"`
}

// EnergyTotals are the gateway's energy counters.
type EnergyTotals struct {
	GridImportKWh       float64 `json:"gridImportKWh"`
	GridExportKWh       float64 `json:"gridExportKWh"`
	SolarKWh            float64 `json:"solarKWh"`
	GeneratorKWh        float64 `json:"generatorKWh"`
	BatteryChargeKWh    float64 `json:"batteryChargeKWh"`
	BatteryDischargeKWh float64 `json:"batteryDischargeKWh"`
	HomeKWh             float64 `json:"homeKWh"`
}

// GridLimits is returned by Client.GridLimits and Client.UpdateGridLimits.
type GridLimits struct {
	GatewayID     string  `json:"gatewayId"`
	ImportKW      float64 `json:"importKW"`
	ImportLimited bool    `json:"importLimited"`
	ExportKW      float64 `json:"exportKW"`
	ExportLimited bool    `json:"exportLimited"`
	// Applied is set by UpdateGridLimits: false means the server accepted
	// the change but still reports the old limits (shown here).
	Applied *bool `json:"applied,omitempty"`
}
