package gomodstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultMirrorURL      = "https://proxy.golang.org"
	DefaultPublishTimeout = 5 * time.Minute
	DefaultPollInterval   = 2 * time.Second
)

type PublishOptions struct {
	ServerURL    string
	ModulePrefix string
	MirrorURL    string
	Body         io.Reader
	HTTPClient   *http.Client
	Timeout      time.Duration
	PollInterval time.Duration
}

type PublishResult struct {
	Module  string
	Version string
	URLs    map[string]string
}

type GetOptions struct {
	MirrorURL  string
	Module     string
	Output     io.Writer
	HTTPClient *http.Client
}

func Publish(ctx context.Context, opts PublishOptions) (*PublishResult, error) {
	if opts.ServerURL == "" {
		return nil, errors.New("server URL is required")
	}
	if err := ValidateModulePrefix(opts.ModulePrefix); err != nil {
		return nil, err
	}
	if opts.Body == nil {
		return nil, errors.New("publish body is required")
	}
	if opts.MirrorURL == "" {
		opts.MirrorURL = DefaultMirrorURL
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultPublishTimeout
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	endpoint, err := publishEndpoint(opts.ServerURL, opts.ModulePrefix)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, opts.Body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("publish failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	dec := json.NewDecoder(resp.Body)
	var event PublishEvent
	if err := dec.Decode(&event); err != nil {
		return nil, fmt.Errorf("reading ready event: %w", err)
	}
	if event.Event == "error" {
		return nil, errors.New(event.Error)
	}
	if event.Event != "ready" {
		return nil, fmt.Errorf("expected ready event, got %q", event.Event)
	}
	if err := ValidateModulePath(event.Module); err != nil {
		return nil, fmt.Errorf("server returned invalid module path: %w", err)
	}
	if event.Version == "" {
		event.Version = ModuleVersion
	}
	if event.Version != ModuleVersion {
		return nil, fmt.Errorf("server returned unsupported module version %q", event.Version)
	}

	if err := WaitForMirror(ctx, opts.HTTPClient, opts.MirrorURL, event.Module, event.Version, opts.PollInterval); err != nil {
		return nil, err
	}

	for {
		event = PublishEvent{}
		if err := dec.Decode(&event); err != nil {
			return nil, fmt.Errorf("waiting for cached event: %w", err)
		}
		switch event.Event {
		case "cached":
			urls, err := ArtifactURLs(opts.MirrorURL, event.Module, event.Version)
			if err != nil {
				return nil, err
			}
			return &PublishResult{Module: event.Module, Version: event.Version, URLs: urls}, nil
		case "error":
			return nil, errors.New(event.Error)
		default:
			return nil, fmt.Errorf("unexpected publish event %q", event.Event)
		}
	}
}

func WaitForMirror(ctx context.Context, client *http.Client, mirrorURL, module, version string, pollInterval time.Duration) error {
	if client == nil {
		client = http.DefaultClient
	}
	if mirrorURL == "" {
		mirrorURL = DefaultMirrorURL
	}
	if pollInterval <= 0 {
		pollInterval = DefaultPollInterval
	}
	pending := map[string]bool{
		"info": true,
		"mod":  true,
		"zip":  true,
	}

	for len(pending) > 0 {
		for _, kind := range ArtifactKinds {
			if !pending[kind] {
				continue
			}
			ok, err := mirrorHasArtifact(ctx, client, mirrorURL, module, version, kind)
			if err != nil {
				return err
			}
			if ok {
				delete(pending, kind)
			}
		}
		if len(pending) == 0 {
			return nil
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func Get(ctx context.Context, opts GetOptions) (*Manifest, error) {
	if opts.MirrorURL == "" {
		opts.MirrorURL = DefaultMirrorURL
	}
	if opts.Output == nil {
		return nil, errors.New("output writer is required")
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if err := ValidateModulePath(opts.Module); err != nil {
		return nil, err
	}
	zipURL, err := ArtifactURL(opts.MirrorURL, opts.Module, ModuleVersion, "zip")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zipURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("download failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	zipBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	payload, manifest, err := DecodeModuleZip(zipBytes)
	if err != nil {
		return nil, err
	}
	if manifest.Module != opts.Module {
		return nil, fmt.Errorf("zip module %q does not match requested module %q", manifest.Module, opts.Module)
	}
	if _, err := opts.Output.Write(payload); err != nil {
		return nil, err
	}
	return manifest, nil
}

func MirrorURLs(mirrorURL, module string) ([]string, error) {
	if mirrorURL == "" {
		mirrorURL = DefaultMirrorURL
	}
	if err := ValidateModulePath(module); err != nil {
		return nil, err
	}
	urls := make([]string, 0, len(ArtifactKinds))
	for _, kind := range ArtifactKinds {
		u, err := ArtifactURL(mirrorURL, module, ModuleVersion, kind)
		if err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}
	return urls, nil
}

func publishEndpoint(serverURL, prefix string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/publish"
	q := u.Query()
	q.Set("module_prefix", prefix)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func mirrorHasArtifact(ctx context.Context, client *http.Client, mirrorURL, module, version, kind string) (bool, error) {
	u, err := ArtifactURL(mirrorURL, module, version, kind)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound, http.StatusGone:
		return false, nil
	default:
		return false, nil
	}
}
