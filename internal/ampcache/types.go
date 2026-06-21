package ampcache

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultCacheDomain    = "cdn.ampproject.org"
	DefaultPublishTimeout = 2 * time.Minute
	DefaultPollInterval   = 2 * time.Second
)

type Encoding string

const (
	EncodingHTML  Encoding = "html"
	EncodingFont  Encoding = "font"
	EncodingImage Encoding = "image"
)

var encodings = map[Encoding]bool{
	EncodingHTML:  true,
	EncodingFont:  true,
	EncodingImage: true,
}

type Manifest struct {
	Encoding    string `json:"encoding"`
	ResourceURL string `json:"resource_url"`
	Length      int64  `json:"length"`
	SHA256      string `json:"sha256"`
}

type PublishEvent struct {
	Event         string `json:"event"`
	Encoding      string `json:"encoding,omitempty"`
	ResourceURL   string `json:"resource_url,omitempty"`
	CacheURL      string `json:"cache_url,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	Length        int64  `json:"length,omitempty"`
	ResourceBytes int64  `json:"resource_bytes,omitempty"`
	FetchCount    int    `json:"fetch_count,omitempty"`
	UserAgent     string `json:"user_agent,omitempty"`
	Error         string `json:"error,omitempty"`
}

func ParseEncoding(value string) (Encoding, error) {
	enc := Encoding(strings.ToLower(strings.TrimSpace(value)))
	if !encodings[enc] {
		return "", fmt.Errorf("unsupported encoding %q", value)
	}
	return enc, nil
}

func ValidateEncoding(enc Encoding) error {
	if !encodings[enc] {
		return errors.New("unsupported encoding")
	}
	return nil
}
