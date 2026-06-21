package ampcache

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

func ValidateResourceURL(publicURL, resourceURL string, enc Encoding) (*url.URL, error) {
	if err := ValidateEncoding(enc); err != nil {
		return nil, err
	}
	base, err := parsePublicURL(publicURL)
	if err != nil {
		return nil, err
	}
	resource, err := url.Parse(resourceURL)
	if err != nil {
		return nil, err
	}
	if resource.Scheme == "" || resource.Host == "" {
		return nil, errors.New("resource URL must be absolute")
	}
	if resource.Scheme != "http" && resource.Scheme != "https" {
		return nil, errors.New("resource URL scheme must be http or https")
	}
	if resource.Scheme != base.Scheme || !strings.EqualFold(resource.Host, base.Host) {
		return nil, errors.New("resource URL must be under public URL origin")
	}
	if resource.Fragment != "" {
		return nil, errors.New("resource URL must not include a fragment")
	}
	if !isASCII(resource.Host) {
		return nil, errors.New("resource URL host must be ASCII")
	}
	basePath := cleanPrefixPath(base.Path)
	resourcePath := resource.EscapedPath()
	if resourcePath == "" {
		resourcePath = "/"
	}
	if basePath != "/" && resourcePath != basePath && !strings.HasPrefix(resourcePath, basePath+"/") {
		return nil, errors.New("resource URL path must be under public URL path")
	}
	if resourcePath == "/v1/publish" || resourcePath == "/v1/confirm" || strings.HasPrefix(resourcePath, "/v1/") {
		return nil, errors.New("resource URL must not use the API path")
	}
	if err := validateResourceExtension(resource.Path, enc); err != nil {
		return nil, err
	}
	return resource, nil
}

func CreateCacheURL(cacheDomain, resourceURL string, enc Encoding) (string, error) {
	if cacheDomain == "" {
		cacheDomain = DefaultCacheDomain
	}
	if err := ValidateEncoding(enc); err != nil {
		return "", err
	}
	resource, err := url.Parse(resourceURL)
	if err != nil {
		return "", err
	}
	if resource.Scheme != "http" && resource.Scheme != "https" || resource.Host == "" {
		return "", errors.New("resource URL must be absolute http or https")
	}
	if !isASCII(resource.Host) {
		return "", errors.New("resource URL host must be ASCII")
	}
	if err := validateResourceExtension(resource.Path, enc); err != nil {
		return "", err
	}
	subdomain := curlsSubdomain(resource.Hostname())
	class := cacheClass(enc)
	cachePath := "/" + class + "/"
	if resource.Scheme == "https" {
		cachePath += "s/"
	}
	cachePath += resource.Host + resource.EscapedPath()
	if resource.EscapedPath() == "" {
		cachePath += "/"
	}
	cache := url.URL{
		Scheme:   "https",
		Host:     subdomain + "." + strings.TrimSuffix(cacheDomain, "."),
		Path:     cachePath,
		RawQuery: resource.RawQuery,
	}
	return cache.String(), nil
}

func ResourceKey(u *url.URL) string {
	key := u.EscapedPath()
	if key == "" {
		key = "/"
	}
	if u.RawQuery != "" {
		key += "?" + u.RawQuery
	}
	return key
}

func parsePublicURL(value string) (*url.URL, error) {
	if value == "" {
		return nil, errors.New("public URL is required")
	}
	u, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("public URL must be absolute http or https")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("public URL must not include query or fragment")
	}
	if !isASCII(u.Host) {
		return nil, errors.New("public URL host must be ASCII")
	}
	return u, nil
}

func cleanPrefixPath(p string) string {
	if p == "" {
		return "/"
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(p, "/"))
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

func validateResourceExtension(p string, enc Encoding) error {
	ext := strings.ToLower(path.Ext(p))
	switch enc {
	case EncodingHTML:
		if ext == "" || ext == ".html" || ext == ".htm" {
			return nil
		}
		return errors.New("html encoding requires an extensionless, .html, or .htm resource URL")
	case EncodingFont:
		switch ext {
		case ".woff2", ".woff", ".ttf", ".otf", ".eot":
			return nil
		default:
			return errors.New("font encoding requires a font-like resource URL extension")
		}
	case EncodingImage:
		if ext == ".png" {
			return nil
		}
		return errors.New("image encoding requires a .png resource URL")
	default:
		return errors.New("unsupported encoding")
	}
}

func inferEncodingFromResourcePath(p string) (Encoding, error) {
	ext := strings.ToLower(path.Ext(p))
	switch ext {
	case ".woff2", ".woff", ".ttf", ".otf", ".eot":
		return EncodingFont, nil
	case ".png":
		return EncodingImage, nil
	case "", ".html", ".htm":
		return EncodingHTML, nil
	default:
		return "", errors.New("resource URL extension does not match a supported encoding")
	}
}

func cacheClass(enc Encoding) string {
	switch enc {
	case EncodingFont:
		return "r"
	case EncodingImage:
		return "i"
	default:
		return "c"
	}
}

func curlsSubdomain(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	readable := strings.ReplaceAll(strings.ReplaceAll(host, "-", "--"), ".", "-")
	if len(readable) <= 63 && strings.Contains(host, ".") && validReadableSubdomain(readable) {
		return readable
	}
	sum := sha256.Sum256([]byte(host))
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
}

func validReadableSubdomain(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

func isASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] >= 0x80 {
			return false
		}
	}
	return true
}

func URLs(cacheDomain, resourceURL string, enc Encoding) (origin string, cache string, err error) {
	cache, err = CreateCacheURL(cacheDomain, resourceURL, enc)
	if err != nil {
		return "", "", fmt.Errorf("create cache URL: %w", err)
	}
	return resourceURL, cache, nil
}
