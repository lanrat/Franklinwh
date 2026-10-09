package franklinwh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrUnauthorized is matched (with errors.Is) by errors caused by a
// missing, invalid or expired login token. Log in again to recover.
var ErrUnauthorized = errors.New("franklinwh: not logged in or token expired")

// ErrRateLimited is matched (with errors.Is) by errors caused by the server
// rejecting a request as too frequent ("Too Many Requests"). Wait and retry
// less often.
var ErrRateLimited = errors.New("franklinwh: rate limited by the server")

// codeOK is the "code" of a successful response.
const codeOK = 200

// maxResponseSize bounds how much of a response body is read.
const maxResponseSize = 16 << 20

// Response is the JSON envelope returned by the API:
//
//	{"code": 200, "message": "Query success!", "result": {...}, "total": 1, "success": true}
type Response struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Result  json.RawMessage `json:"result"`
	Total   int             `json:"total"`
	Success bool            `json:"success"`
}

// Error is returned when the server rejects a request or reports a failure.
type Error struct {
	Path       string // API path, e.g. /hes-gateway/terminal/getHomeGatewayList
	HTTPStatus int    // HTTP status code
	Code       int    // "code" from the response body, 0 if absent
	Message    string // "message" from the response body
}

func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.HTTPStatus)
	}
	return fmt.Sprintf("franklinwh: %s: %s (http %d, code %d)", e.Path, msg, e.HTTPStatus, e.Code)
}

// Is makes errors.Is(err, ErrUnauthorized) true for authentication
// failures (the app treats HTTP 401 / code 401 as an invalid token) and
// errors.Is(err, ErrRateLimited) true for HTTP 429 / code 429. The server
// usually reports both with HTTP 200 and the code in the body.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.HTTPStatus == http.StatusUnauthorized || e.Code == http.StatusUnauthorized
	case ErrRateLimited:
		return e.HTTPStatus == http.StatusTooManyRequests || e.Code == http.StatusTooManyRequests
	}
	return false
}

// request describes one API call.
type request struct {
	method      string
	path        string // absolute path on the API host, e.g. /hes-gateway/terminal/...
	query       url.Values
	body        []byte
	contentType string
	header      map[string]string // extra headers, names sent verbatim
}

// Do sends an authenticated request to an arbitrary API path, such as
// "/hes-gateway/terminal/selectOffgrid", and returns the decoded envelope.
// A non-nil body is sent as JSON. Do is meant for endpoints this package
// does not wrap; docs/API.md lists many of them.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any) (*Response, error) {
	r := request{method: method, path: path, query: query}
	if body != nil {
		b, err := marshalJSON(body)
		if err != nil {
			return nil, err
		}
		r.body = b
		r.contentType = "application/json"
	}
	return c.do(ctx, r)
}

func (c *Client) do(ctx context.Context, r request) (*Response, error) {
	if !strings.HasPrefix(r.path, "/") {
		return nil, fmt.Errorf("franklinwh: API path %q must start with /", r.path)
	}
	u := c.baseURL + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, u, body)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req.Header)
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	for k, v := range r.header {
		req.Header[k] = []string{v}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("franklinwh: %s %s: %w", r.method, r.path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("franklinwh: reading %s response: %w", r.path, err)
	}
	return parseResponse(r.path, resp.StatusCode, data)
}

// setHeaders adds the headers the app sends with every request. Header
// names keep the app's capitalization.
func (c *Client) setHeaders(h http.Header) {
	set := func(k, v string) { h[k] = []string{v} }
	if t := c.Token(); t != "" {
		set("loginToken", t)
	}
	set("lang", c.lang)
	set("softwareVersion", "APP"+c.appVersion)
	set("optTime", c.now().Format("2006-01-02 15:04:05"))
	set("optSource", "3")
	set("optDevice", c.device.Model)
	set("optDeviceName", c.device.Name)
	set("optSystemVersion", "Android "+c.device.OSVersion)
	h.Set("Accept", "application/json")
	h.Set("User-Agent", c.userAgent)
}

// parseResponse decodes the envelope and turns failures into *Error.
func parseResponse(path string, status int, data []byte) (*Response, error) {
	var env struct {
		Code    *int            `json:"code"`
		Message string          `json:"message"`
		Msg     string          `json:"msg"`
		Result  json.RawMessage `json:"result"`
		Data    json.RawMessage `json:"data"`
		Total   int             `json:"total"`
		Success bool            `json:"success"`
	}
	if err := decodeLenient(data, &env); err != nil {
		if status != http.StatusOK {
			return nil, &Error{Path: path, HTTPStatus: status, Message: snippet(data)}
		}
		return nil, fmt.Errorf("franklinwh: %s: decoding response: %w", path, err)
	}
	r := &Response{Message: env.Message, Result: env.Result, Total: env.Total, Success: env.Success}
	if env.Code != nil {
		r.Code = *env.Code
	}
	if r.Message == "" {
		r.Message = env.Msg
	}
	if len(r.Result) == 0 {
		r.Result = env.Data
	}
	if status != http.StatusOK || !(r.Code == codeOK || r.Success) {
		return r, &Error{Path: path, HTTPStatus: status, Code: r.Code, Message: r.Message}
	}
	return r, nil
}

// decodeLenient unmarshals JSON into out but skips values whose JSON type
// does not match the Go field type instead of failing: the API is
// undocumented and field types occasionally differ between firmware
// versions. encoding/json fills every other field in that case.
func decodeLenient(data []byte, out any) error {
	err := json.Unmarshal(data, out)
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return nil
	}
	return err
}

// decodeResult decodes resp.Result into out.
func decodeResult(resp *Response, out any) error {
	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil
	}
	return decodeLenient(resp.Result, out)
}

// marshalJSON encodes v like Dart's jsonEncode: compact, with no HTML
// escaping and no trailing newline.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// snippet returns the start of a response body for error messages.
func snippet(b []byte) string {
	const n = 200
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}
