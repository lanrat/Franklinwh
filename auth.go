package franklinwh

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Account types, sent as the "type" field of a login request. Homeowners
// (the FranklinWH app's default) should use AccountHomeowner.
const (
	AccountHomeowner  = 0
	AccountInstaller  = 2
	AccountInstaller3 = 3
)

// MFA method names returned by the server and accepted by VerifyMFA.
const (
	MFATOTP     = "TOTP"      // authenticator app
	MFAEmailOTP = "EMAIL_OTP" // one-time code emailed to the user
)

// ErrMFARequired is returned by Login when the account has multi-factor
// authentication enabled. The returned LoginResult carries the MFAToken and
// the available methods; finish with SendEmailOTP (if needed) and VerifyMFA.
var ErrMFARequired = errors.New("franklinwh: multi-factor authentication required")

// LoginResult holds the outcome of a login attempt.
type LoginResult struct {
	// Token is the login token (sent as the loginToken header). It is empty
	// when MFA is still required.
	Token string `json:"token"`

	UserID      json.Number `json:"userId"`
	LoginName   string      `json:"loginName"`
	Email       string      `json:"email"`
	FirstLogin  bool        `json:"firstLogin"`
	CountryID   json.Number `json:"countryId"`
	NeedConsent bool        `json:"needConsent"`

	// MFA fields, populated when MFARequired is true.
	MFARequired  bool     `json:"mfaRequired"`
	MFAToken     string   `json:"mfaToken"`
	MFAMethod    string   `json:"mfaMethod"` // the account's default method
	AvailableMFA []string `json:"availableMfaMethods"`
	MaskedEmail  string   `json:"maskedEmail"`
	LockSeconds  int      `json:"lockSeconds"`
}

// LoginOptions tunes a login request. The zero value logs a homeowner in
// with no CAPTCHA.
type LoginOptions struct {
	// AccountType is one of the Account* constants. Zero means homeowner.
	AccountType int
	// Timezone, e.g. "America/Chicago". Optional; detected by the app but
	// only recorded by the server.
	Timezone string
	// CaptchaID and CaptchaCode answer a CAPTCHA challenge (see PreLogin).
	// Usually unnecessary.
	CaptchaID   string
	CaptchaCode string
}

// loginKey is the static 16-byte AES key the app uses to encrypt the
// password (literal "B6F3Zu&aW8XlH9CA" in the binary). The ciphertext is
// never real protection — it is obfuscation over TLS — but the server
// expects it.
var loginKey = []byte("B6F3Zu&aW8XlH9CA")

// Login authenticates with an email and password. On success the token is
// stored on the Client and returned. If the account requires MFA, Login
// returns ErrMFARequired together with a non-nil *LoginResult; call
// SendEmailOTP (for EMAIL_OTP) and then VerifyMFA.
func (c *Client) Login(ctx context.Context, account, password string, opts *LoginOptions) (*LoginResult, error) {
	if opts == nil {
		opts = &LoginOptions{}
	}
	enc, err := encryptPassword(password)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"account":          account,
		"password":         enc,
		"type":             opts.AccountType,
		"softwareVersion":  "APP" + c.appVersion,
		"optDevice":        c.device.Model,
		"optSystemVersion": "Android " + c.device.OSVersion,
		"userType":         "",
		"captchaId":        opts.CaptchaID,
		"captchaCode":      opts.CaptchaCode,
		"timezone":         opts.Timezone,
		"enc":              "1", // tells the server the password is AES-encrypted
	}
	resp, err := c.do(ctx, request{
		method:      "POST",
		path:        gatewayPrefix + "/terminal/initialize/appUserOrInstallerLogin",
		body:        mustJSON(body),
		contentType: "application/json",
		header:      map[string]string{"client-id": c.clientID},
	})
	if err != nil {
		return nil, err
	}
	var out LoginResult
	if err := decodeResult(resp, &out); err != nil {
		return nil, fmt.Errorf("franklinwh: decoding login result: %w", err)
	}
	if out.MFARequired {
		return &out, ErrMFARequired
	}
	if out.Token == "" {
		return &out, errors.New("franklinwh: login succeeded but no token was returned")
	}
	c.SetToken(out.Token)
	return &out, nil
}

// SendEmailOTP asks the server to email a one-time code for an MFA login in
// progress. Pass the MFAToken from the ErrMFARequired LoginResult.
func (c *Client) SendEmailOTP(ctx context.Context, mfaToken string) error {
	_, err := c.do(ctx, request{
		method:      "POST",
		path:        gatewayPrefix + "/terminal/initialize/mfa/email-otp/login/send",
		body:        mustJSON(map[string]any{"mfaToken": mfaToken}),
		contentType: "application/json",
	})
	return err
}

// VerifyMFA completes an MFA login with the code from the user's
// authenticator app (method MFATOTP) or email (method MFAEmailOTP). On
// success the token is stored on the Client and returned. Set remember to
// skip MFA on this client ID next time.
func (c *Client) VerifyMFA(ctx context.Context, mfaToken, method, code string, remember bool) (*LoginResult, error) {
	body := map[string]any{
		"mfaToken":          mfaToken,
		"code":              code,
		"method":            method,
		"deviceFingerprint": c.clientID,
		"rememberDevice":    remember,
	}
	resp, err := c.do(ctx, request{
		method:      "POST",
		path:        gatewayPrefix + "/terminal/initialize/mfa/login/verify",
		body:        mustJSON(body),
		contentType: "application/json",
	})
	if err != nil {
		return nil, err
	}
	var out LoginResult
	if err := decodeResult(resp, &out); err != nil {
		return nil, fmt.Errorf("franklinwh: decoding MFA result: %w", err)
	}
	if out.Token == "" {
		return &out, errors.New("franklinwh: MFA verification returned no token")
	}
	c.SetToken(out.Token)
	return &out, nil
}

// Logout invalidates the current token on the server and clears it locally.
func (c *Client) Logout(ctx context.Context) error {
	_, err := c.do(ctx, request{method: "POST", path: gatewayPrefix + "/terminal/v2/loginOut"})
	c.SetToken("")
	return err
}

// encryptPassword reproduces the app's password obfuscation. The app:
//  1. MD5-hashes the password and lowercase-hex-encodes it (32 chars),
//  2. derives a 16-byte AES key as the middle 16 chars of that hash,
//  3. AES-CBC/PKCS7 encrypts the 32-char hash with a random 16-byte IV,
//  4. sends base64(ciphertext) + ":" + base64(iv).
//
// A fixed server-side key would be just as (in)secure; this simply matches
// what the server validates.
func encryptPassword(password string) (string, error) {
	sum := md5.Sum([]byte(password))
	hexHash := hex.EncodeToString(sum[:]) // 32 lowercase hex chars
	key := aesKeyFrom(hexHash)
	return encryptAESCBC([]byte(hexHash), key)
}

// aesKeyFrom returns the middle 16 bytes of s (matching the app's
// _getAESKey): for a string of length n >= 16 it is s[(n-16)/2 : (n-16)/2+16].
// Shorter strings are returned unchanged.
func aesKeyFrom(s string) []byte {
	if len(s) < 16 {
		return []byte(s)
	}
	start := (len(s) - 16) / 2
	return []byte(s[start : start+16])
}

// encryptAESCBC AES-CBC/PKCS7 encrypts plaintext with a random IV and
// returns base64(ciphertext) + ":" + base64(iv).
func encryptAESCBC(plaintext, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("franklinwh: AES key: %w", err)
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("franklinwh: generating IV: %w", err)
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	b64 := base64.StdEncoding
	return b64.EncodeToString(ct) + ":" + b64.EncodeToString(iv), nil
}

// pkcs7Pad appends PKCS#7 padding to make len(data) a multiple of blockSize.
func pkcs7Pad(data []byte, blockSize int) []byte {
	n := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+n)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(n)
	}
	return out
}

// mustJSON marshals v or panics; used for request bodies this package
// controls, where marshaling cannot fail.
func mustJSON(v any) []byte {
	b, err := marshalJSON(v)
	if err != nil {
		panic("franklinwh: marshaling request: " + err.Error())
	}
	return b
}
