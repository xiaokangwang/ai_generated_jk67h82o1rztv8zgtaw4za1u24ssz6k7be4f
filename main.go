package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type StargazerResponse struct {
	Login string `json:"login"`
}

func main() {
	outputFile := flag.String("o", "", "Output file path (optional)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <owner/repo>\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Example: %s -o stargazers.txt golang/go\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	repo := flag.Arg(0)
	token := os.Getenv("GITHUB_TOKEN")

	if token == "" {
		fmt.Println("Warning: GITHUB_TOKEN not set. You may hit rate limits quickly.")
		fmt.Println("Set it with: export GITHUB_TOKEN=your_token_here")
	}

	fmt.Printf("Fetching stargazers for %s...\n\n", repo)

	stargazers, err := fetchStargazers(repo, token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Display to stdout
	fmt.Printf("Found %d stargazers:\n\n", len(stargazers))
	for i, username := range stargazers {
		fmt.Printf("%d. %s\n", i+1, username)
	}

	// Write to file if specified
	if *outputFile != "" {
		if err := writeToFile(*outputFile, stargazers); err != nil {
			fmt.Fprintf(os.Stderr, "\nError writing to file: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nUsernames written to: %s\n", *outputFile)
	}
}

func writeToFile(filename string, usernames []string) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("creating file: %w", err)
	}
	defer file.Close()

	content := strings.Join(usernames, "\n") + "\n"
	if _, err := file.WriteString(content); err != nil {
		return fmt.Errorf("writing to file: %w", err)
	}

	return nil
}

func fetchStargazers(repo string, token string) ([]string, error) {
	var allStargazers []string
	page := 1
	perPage := 100

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	for {
		url := fmt.Sprintf("https://api.github.com/repos/%s/stargazers?page=%d&per_page=%d", repo, page, perPage)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}

		req.Header.Set("Accept", "application/vnd.github.v3+json")
		if token != "" {
			req.Header.Set("Authorization", "token "+token)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching page %d: %w", page, err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading response: %w", err)
		}

		var stargazers []StargazerResponse
		if err := json.Unmarshal(body, &stargazers); err != nil {
			return nil, fmt.Errorf("parsing JSON: %w", err)
		}

		if len(stargazers) == 0 {
			break
		}

		for _, sg := range stargazers {
			allStargazers = append(allStargazers, sg.Login)
		}

		fmt.Printf("Fetched page %d (%d users)...\n", page, len(stargazers))

		if len(stargazers) < perPage {
			break
		}

		page++
		time.Sleep(1 * time.Second) // Rate limiting courtesy
	}

	return allStargazers, nil
}
