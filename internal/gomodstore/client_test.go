package gomodstore

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientPublishUsesMirrorAndWaitsForCached(t *testing.T) {
	store := NewServer(ServerConfig{
		Timeout:   2 * time.Second,
		ChunkSize: 5,
	})
	storeServer := httptest.NewServer(store)
	defer storeServer.Close()

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originURL := storeServer.URL + "/proxy" + r.URL.EscapedPath()
		resp, err := http.Get(originURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer mirror.Close()

	payload := []byte("client publish payload")
	result, err := Publish(context.Background(), PublishOptions{
		ServerURL:    storeServer.URL,
		ModulePrefix: "example.com/store",
		MirrorURL:    mirror.URL,
		Body:         bytes.NewReader(payload),
		Timeout:      2 * time.Second,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}
	wantModule, err := ModulePathForPayload("example.com/store", payload)
	if err != nil {
		t.Fatalf("ModulePathForPayload returned error: %v", err)
	}
	if result.Module != wantModule {
		t.Fatalf("result module = %q, want %q", result.Module, wantModule)
	}
	if result.URLs["zip"] == "" || result.URLs["mod"] == "" || result.URLs["info"] == "" {
		t.Fatalf("result URLs were incomplete: %#v", result.URLs)
	}
	waitFor(t, func() bool { return store.ActiveCount() == 0 })
}

func TestClientGetDownloadsAndValidatesManifest(t *testing.T) {
	payload := []byte("download me from mirror")
	artifacts, err := BuildArtifacts("example.com/store", payload, time.Now(), 6)
	if err != nil {
		t.Fatalf("BuildArtifacts returned error: %v", err)
	}
	zipURLPath := "/" + artifacts.Module + "/@v/" + ModuleVersion + ".zip"
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != zipURLPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(artifacts.Zip)
	}))
	defer mirror.Close()

	var out bytes.Buffer
	manifest, err := Get(context.Background(), GetOptions{
		MirrorURL: mirror.URL,
		Module:    artifacts.Module,
		Output:    &out,
	})
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("downloaded payload = %q, want %q", out.Bytes(), payload)
	}
	if manifest.SHA256 != artifacts.Manifest.SHA256 {
		t.Fatalf("manifest hash = %q, want %q", manifest.SHA256, artifacts.Manifest.SHA256)
	}
}

func TestMirrorURLs(t *testing.T) {
	module, err := ModulePathForPayload("example.com/store", []byte("urls"))
	if err != nil {
		t.Fatalf("ModulePathForPayload returned error: %v", err)
	}
	urls, err := MirrorURLs("https://proxy.example", module)
	if err != nil {
		t.Fatalf("MirrorURLs returned error: %v", err)
	}
	if len(urls) != 3 {
		t.Fatalf("len(urls) = %d, want 3", len(urls))
	}
	for i, kind := range ArtifactKinds {
		want, err := ArtifactURL("https://proxy.example", module, ModuleVersion, kind)
		if err != nil {
			t.Fatalf("ArtifactURL returned error: %v", err)
		}
		if urls[i] != want {
			t.Fatalf("urls[%d] = %q, want %q", i, urls[i], want)
		}
	}
}
