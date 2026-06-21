package gomodstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	ModuleVersion    = "v0.0.0"
	DefaultChunkSize = 256 * 1024
)

var ArtifactKinds = []string{"info", "mod", "zip"}

func PayloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func ModulePathForPayload(prefix string, payload []byte) (string, error) {
	if err := ValidateModulePrefix(prefix); err != nil {
		return "", err
	}
	return prefix + "/" + PayloadHash(payload), nil
}

func ValidateModulePrefix(prefix string) error {
	if prefix == "" {
		return errors.New("module prefix is empty")
	}
	if strings.Contains(prefix, "://") {
		return errors.New("module prefix must not include a URL scheme")
	}
	if strings.HasPrefix(prefix, "/") || strings.HasSuffix(prefix, "/") {
		return errors.New("module prefix must not start or end with /")
	}
	if strings.Contains(prefix, "//") {
		return errors.New("module prefix must not contain empty path segments")
	}
	if strings.ContainsAny(prefix, "@!\\") {
		return errors.New("module prefix contains a disallowed character")
	}

	parts := strings.Split(prefix, "/")
	if len(parts) == 0 || !strings.Contains(parts[0], ".") {
		return errors.New("module prefix first path element must contain a dot")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errors.New("module prefix contains an invalid path segment")
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
				continue
			}
			return fmt.Errorf("module prefix contains invalid byte %q", c)
		}
	}
	for _, label := range strings.Split(parts[0], ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return errors.New("module prefix has an invalid domain label")
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
				continue
			}
			return errors.New("module prefix has an invalid domain label")
		}
	}
	return nil
}

func ValidateModulePath(module string) error {
	prefix, hash, err := SplitModulePath(module)
	if err != nil {
		return err
	}
	if err := ValidateModulePrefix(prefix); err != nil {
		return err
	}
	if len(hash) != sha256.Size*2 {
		return errors.New("module path hash must be a SHA-256 hex string")
	}
	for i := 0; i < len(hash); i++ {
		c := hash[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return errors.New("module path hash must be lowercase hex")
	}
	return nil
}

func SplitModulePath(module string) (prefix string, hash string, err error) {
	if module == "" {
		return "", "", errors.New("module path is empty")
	}
	i := strings.LastIndex(module, "/")
	if i <= 0 || i == len(module)-1 {
		return "", "", errors.New("module path must be <prefix>/<sha256>")
	}
	return module[:i], module[i+1:], nil
}

func EscapeModulePath(module string) (string, error) {
	if module == "" {
		return "", errors.New("module path is empty")
	}
	var b strings.Builder
	b.Grow(len(module))
	for i := 0; i < len(module); i++ {
		c := module[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte('!')
			b.WriteByte(c + ('a' - 'A'))
		case c == '!' || c >= 0x80:
			return "", fmt.Errorf("module path contains unescapable byte %q", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}

func UnescapeModulePath(escaped string) (string, error) {
	if escaped == "" {
		return "", errors.New("module path is empty")
	}
	var b strings.Builder
	b.Grow(len(escaped))
	for i := 0; i < len(escaped); i++ {
		c := escaped[i]
		if c != '!' {
			if c >= 0x80 {
				return "", fmt.Errorf("module path contains invalid byte %q", c)
			}
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(escaped) {
			return "", errors.New("module path has dangling ! escape")
		}
		next := escaped[i]
		if next < 'a' || next > 'z' {
			return "", errors.New("module path has invalid ! escape")
		}
		b.WriteByte(next - ('a' - 'A'))
	}
	return b.String(), nil
}

func ArtifactURL(baseURL, module, version, kind string) (string, error) {
	if kind != "info" && kind != "mod" && kind != "zip" {
		return "", fmt.Errorf("unknown artifact kind %q", kind)
	}
	escaped, err := EscapeModulePath(module)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(baseURL, "/") + "/" + escaped + "/@v/" + version + "." + kind, nil
}

func ArtifactURLs(baseURL, module, version string) (map[string]string, error) {
	urls := make(map[string]string, len(ArtifactKinds))
	for _, kind := range ArtifactKinds {
		u, err := ArtifactURL(baseURL, module, version, kind)
		if err != nil {
			return nil, err
		}
		urls[kind] = u
	}
	return urls, nil
}
