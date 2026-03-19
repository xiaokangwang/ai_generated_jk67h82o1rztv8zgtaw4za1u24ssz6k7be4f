package transfer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

type Listing struct {
	RequestedPath string         `json:"requested_path"`
	RootIsDir     bool           `json:"root_is_dir"`
	RootSize      int64          `json:"root_size,omitempty"`
	Entries       []ListingEntry `json:"entries"`
}

type ListingEntry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size,omitempty"`
}

func BuildListing(rootPath string) (*Listing, error) {
	info, err := os.Stat(rootPath)
	if err != nil {
		return nil, err
	}

	listing := &Listing{
		RequestedPath: rootPath,
		RootIsDir:     info.IsDir(),
		RootSize:      info.Size(),
		Entries:       make([]ListingEntry, 0),
	}
	if !info.IsDir() {
		return listing, nil
	}

	entries, err := os.ReadDir(rootPath)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		childPath := filepath.Join(rootPath, entry.Name())
		listEntry, ok, err := buildListingEntry(rootPath, childPath, entry)
		if err != nil {
			return nil, err
		}
		if ok {
			listing.Entries = append(listing.Entries, listEntry)
		}
	}

	sort.Slice(listing.Entries, func(i, j int) bool {
		return listing.Entries[i].Path < listing.Entries[j].Path
	})

	return listing, nil
}

func EncodeListing(listing *Listing) ([]byte, error) {
	return json.Marshal(listing)
}

func DecodeListing(data []byte) (*Listing, error) {
	var listing Listing
	if err := json.Unmarshal(data, &listing); err != nil {
		return nil, err
	}
	return &listing, nil
}

func buildListingEntry(rootPath, fullPath string, entry os.DirEntry) (ListingEntry, bool, error) {
	info, err := entry.Info()
	if err != nil {
		return ListingEntry{}, false, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return ListingEntry{}, false, nil
	}

	rel, err := filepath.Rel(rootPath, fullPath)
	if err != nil {
		return ListingEntry{}, false, err
	}

	return ListingEntry{
		Path:  filepath.ToSlash(rel),
		IsDir: info.IsDir(),
		Size:  info.Size(),
	}, true, nil
}
