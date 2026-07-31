package application

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
)

type CursorCodec struct {
	secret []byte
}

type cursorPayload struct {
	Version           int    `json:"v"`
	MerchantID        string `json:"merchant_id"`
	From              string `json:"from,omitempty"`
	To                string `json:"to,omitempty"`
	Limit             int    `json:"limit"`
	HighWaterPosition int64  `json:"high_water_position"`
	LastDate          string `json:"last_date"`
	LastTime          string `json:"last_time"`
	LastID            string `json:"last_id"`
}

func NewCursorCodec(secret []byte) (*CursorCodec, error) {
	if len(secret) < 32 {
		return nil, ErrInvalidArgument
	}
	copySecret := append([]byte(nil), secret...)
	return &CursorCodec{secret: copySecret}, nil
}

func (c *CursorCodec) encode(payload cursorPayload) (string, error) {
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write(encodedPayload)
	signature := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(encodedPayload) + "." +
		base64.RawURLEncoding.EncodeToString(signature), nil
}

func (c *CursorCodec) decode(token string) (cursorPayload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return cursorPayload{}, ErrInvalidCursor
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return cursorPayload{}, ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return cursorPayload{}, ErrInvalidCursor
	}
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write(payloadBytes)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return cursorPayload{}, ErrInvalidCursor
	}
	var payload cursorPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil || payload.Version != 1 {
		return cursorPayload{}, ErrInvalidCursor
	}
	return payload, nil
}
