package franklinwh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// GridLimitLimited is the *Flag value seen alongside a kW limit (the app's
// "Limit" option). The codes for the app's other options ("No limit",
// "No Export") have not been confirmed, so this package only reports them.
const GridLimitLimited = 2

// gridLimitsAppVersion is the app version reported when setting grid
// limits. The server rejects setPowerControlV2 from current app versions
// ("This setting is read-only") but accepts it from older ones.
const gridLimitsAppVersion = "2.6.2"

// GridLimits are the grid import/export power limits of a gateway (the
// app's Power Control System screen). Newer app versions show these as
// read-only for homeowners; the API still exposes them.
type GridLimits struct {
	ImportKW   float64 // max power drawn from the grid (globalGridChargeMax)
	ImportFlag int     // gridMaxFlag; GridLimitLimited when ImportKW applies
	ExportKW   float64 // max power fed to the grid (globalGridDischargeMax)
	ExportFlag int     // gridFeedMaxFlag; GridLimitLimited when ExportKW applies

	// Raw is the full getPowerControlSetting result. SetGridLimits sends it
	// back with only the limit fields changed, so fields this package does
	// not model keep their values.
	Raw map[string]json.RawMessage
}

// GridLimits returns the grid import/export limits for a gateway
// (GET /hes-gateway/terminal/tou/getPowerControlSetting).
func (c *Client) GridLimits(ctx context.Context, gatewayID string) (*GridLimits, error) {
	resp, err := c.do(ctx, request{
		method: "GET",
		path:   gatewayPrefix + "/terminal/tou/getPowerControlSetting",
		query:  url.Values{"gatewayId": {gatewayID}},
	})
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(resp.Result, &raw); err != nil {
		return nil, fmt.Errorf("franklinwh: decoding power control settings: %w", err)
	}
	// The global* fields are the stored limits the app displays; gridMax
	// and gridFeedMax normally match them but can lag after a change.
	var f struct {
		GlobalGridChargeMax    float64 `json:"globalGridChargeMax"`
		GlobalGridDischargeMax float64 `json:"globalGridDischargeMax"`
		GridMaxFlag            int     `json:"gridMaxFlag"`
		GridFeedMaxFlag        int     `json:"gridFeedMaxFlag"`
	}
	if err := decodeLenient(resp.Result, &f); err != nil {
		return nil, fmt.Errorf("franklinwh: decoding power control settings: %w", err)
	}
	return &GridLimits{
		ImportKW:   f.GlobalGridChargeMax,
		ImportFlag: f.GridMaxFlag,
		ExportKW:   f.GlobalGridDischargeMax,
		ExportFlag: f.GridFeedMaxFlag,
		Raw:        raw,
	}, nil
}

// SetGridLimits writes grid import/export limits
// (POST /hes-gateway/terminal/tou/setPowerControlV2). Pass limits returned
// by GridLimits with the fields to change modified; the rest of Raw is sent
// back unchanged.
//
// The server takes the new limits from the request's gridMax (import) and
// gridFeedMax (export) and ignores the global* fields, which this method
// sets to the same values anyway so the body is consistent. The request
// reports an older app version, which the server requires before it will
// change these settings for a homeowner.
func (c *Client) SetGridLimits(ctx context.Context, gatewayID string, l *GridLimits) error {
	if l == nil || l.Raw == nil {
		return errors.New("franklinwh: SetGridLimits needs limits read with GridLimits")
	}
	// The server reports -1 (apparently "no limit"); allow sending back a
	// negative value read from it, but not setting a new one.
	for _, f := range []struct {
		kw  float64
		key string
	}{{l.ImportKW, "globalGridChargeMax"}, {l.ExportKW, "globalGridDischargeMax"}} {
		var old float64
		json.Unmarshal(l.Raw[f.key], &old)
		if f.kw < 0 && f.kw != old {
			return errors.New("franklinwh: grid limits must not be negative")
		}
	}
	body := make(map[string]any, len(l.Raw)+1)
	for k, v := range l.Raw {
		body[k] = v
	}
	body["gatewayId"] = gatewayID
	body["gridMax"] = l.ImportKW
	body["globalGridChargeMax"] = l.ImportKW
	body["gridMaxFlag"] = l.ImportFlag
	body["gridFeedMax"] = l.ExportKW
	body["globalGridDischargeMax"] = l.ExportKW
	body["gridFeedMaxFlag"] = l.ExportFlag
	_, err := c.do(ctx, request{
		method:      "POST",
		path:        gatewayPrefix + "/terminal/tou/setPowerControlV2",
		body:        mustJSON(body),
		contentType: "application/json",
		header:      map[string]string{"softwareVersion": "APP" + gridLimitsAppVersion},
	})
	return err
}

// ErrGridLimitsNotApplied is returned by UpdateGridLimits when the server
// accepts the change but reports the old limits when read back.
var ErrGridLimitsNotApplied = errors.New("franklinwh: the server accepted the grid limits but they did not change")

// Apply sets new limits in l. A nil value leaves that limit unchanged; a
// non-nil one must not be negative and turns the limit on
// (GridLimitLimited). l is not modified when an error is returned.
func (l *GridLimits) Apply(importKW, exportKW *float64) error {
	if importKW != nil && *importKW < 0 {
		return errors.New("franklinwh: import limit must not be negative")
	}
	if exportKW != nil && *exportKW < 0 {
		return errors.New("franklinwh: export limit must not be negative")
	}
	if importKW != nil {
		l.ImportKW, l.ImportFlag = *importKW, GridLimitLimited
	}
	if exportKW != nil {
		l.ExportKW, l.ExportFlag = *exportKW, GridLimitLimited
	}
	return nil
}

// UpdateGridLimits reads a gateway's grid limits, applies the given values
// (see GridLimits.Apply; nil leaves a limit unchanged), writes them, and
// returns the limits read back from the server. If the server reports
// different values afterwards it returns them with ErrGridLimitsNotApplied.
func (c *Client) UpdateGridLimits(ctx context.Context, gatewayID string, importKW, exportKW *float64) (*GridLimits, error) {
	l, err := c.GridLimits(ctx, gatewayID)
	if err != nil {
		return nil, err
	}
	if err := l.Apply(importKW, exportKW); err != nil {
		return nil, err
	}
	if err := c.SetGridLimits(ctx, gatewayID, l); err != nil {
		return nil, err
	}
	after, err := c.GridLimits(ctx, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("franklinwh: limits sent, but re-reading them failed: %w", err)
	}
	if after.ImportKW != l.ImportKW || after.ExportKW != l.ExportKW {
		return after, ErrGridLimitsNotApplied
	}
	return after, nil
}
