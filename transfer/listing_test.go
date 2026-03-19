package transfer

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBuildListingSingleDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "top.txt"), []byte("top"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "child.txt"), []byte("child"), 0o644); err != nil {
		t.Fatal(err)
	}

	listing, err := BuildListing(root)
	if err != nil {
		t.Fatal(err)
	}
	if !listing.RootIsDir {
		t.Fatal("expected root directory")
	}

	got := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		if entry.IsDir {
			got = append(got, "d:"+entry.Path)
			continue
		}
		got = append(got, "f:"+entry.Path)
	}

	want := []string{
		"d:nested",
		"f:top.txt",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected listing entries: got %v want %v", got, want)
	}
}

func TestListingRoundTrip(t *testing.T) {
	t.Parallel()

	listing := &Listing{
		RequestedPath: "/tmp/example",
		RootIsDir:     true,
		Entries: []ListingEntry{
			{Path: "dir", IsDir: true},
			{Path: "dir/file.txt", Size: 42},
		},
	}

	data, err := EncodeListing(listing)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeListing(data)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(decoded, listing) {
		t.Fatalf("decoded listing mismatch: got %#v want %#v", decoded, listing)
	}
}
