package ampcache

import (
	"strings"
	"testing"
)

func TestCreateCacheURLByEncoding(t *testing.T) {
	tests := []struct {
		name string
		enc  Encoding
		url  string
		want string
	}{
		{
			name: "html",
			enc:  EncodingHTML,
			url:  "https://amp.dev/stories",
			want: "https://amp-dev.cdn.ampproject.org/c/s/amp.dev/stories",
		},
		{
			name: "font",
			enc:  EncodingFont,
			url:  "https://www.example.com/fonts/payload.woff2",
			want: "https://www-example-com.cdn.ampproject.org/r/s/www.example.com/fonts/payload.woff2",
		},
		{
			name: "image",
			enc:  EncodingImage,
			url:  "https://www.example.com/images/payload.png",
			want: "https://www-example-com.cdn.ampproject.org/i/s/www.example.com/images/payload.png",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CreateCacheURL(DefaultCacheDomain, test.url, test.enc)
			if err != nil {
				t.Fatalf("CreateCacheURL returned error: %v", err)
			}
			if got != test.want {
				t.Fatalf("CreateCacheURL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateResourceURL(t *testing.T) {
	publicURL := "https://example.com/base"
	valid := map[Encoding]string{
		EncodingHTML:  "https://example.com/base/item.html",
		EncodingFont:  "https://example.com/base/item.woff2",
		EncodingImage: "https://example.com/base/item.png",
	}
	for enc, resourceURL := range valid {
		if _, err := ValidateResourceURL(publicURL, resourceURL, enc); err != nil {
			t.Fatalf("ValidateResourceURL(%s, %s) returned error: %v", enc, resourceURL, err)
		}
	}

	invalid := []struct {
		name string
		enc  Encoding
		url  string
	}{
		{"other-origin", EncodingHTML, "https://other.example/base/item.html"},
		{"outside-prefix", EncodingHTML, "https://example.com/other/item.html"},
		{"api-path", EncodingHTML, "https://example.com/v1/publish.html"},
		{"fragment", EncodingHTML, "https://example.com/base/item.html#x"},
		{"extension", EncodingFont, "https://example.com/base/item.png"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateResourceURL(publicURL, test.url, test.enc)
			if err == nil {
				t.Fatalf("ValidateResourceURL(%q) succeeded, want error", test.url)
			}
			if strings.TrimSpace(err.Error()) == "" {
				t.Fatalf("error was empty")
			}
		})
	}
}

func TestCreateCacheURLRejectsEncodingExtensionMismatch(t *testing.T) {
	if _, err := CreateCacheURL(DefaultCacheDomain, "https://example.com/payload.png", EncodingFont); err == nil {
		t.Fatalf("CreateCacheURL succeeded for font encoding with png URL, want error")
	}
}
