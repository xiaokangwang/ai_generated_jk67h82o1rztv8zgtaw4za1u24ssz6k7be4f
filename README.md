# GitHub Star Crawler

A lightweight Go application to crawl and list usernames of people who have starred a GitHub repository.

## Features

- Fetches all stargazers for any public GitHub repository
- Pagination support (handles repositories with thousands of stars)
- Optional GitHub token authentication for higher rate limits
- Progress reporting during fetch
- **File output support** - Save usernames to a file (in addition to stdout)
- Simple command-line interface

## Installation

### Prerequisites

Go 1.21 or higher is required. If you don't have Go installed, you can install it from [go.dev](https://go.dev/dl/).

For this project, Go is installed at `~/go-toolchain/go/bin/go`

### Build

```bash
~/go-toolchain/go/bin/go build -o github-star-crawler main.go
```

## Usage

### Basic Usage (No Authentication)

```bash
~/go-toolchain/go/bin/go run main.go owner/repo
```

Example:
```bash
~/go-toolchain/go/bin/go run main.go golang/go
```

Note: Without authentication, you're limited to 60 requests per hour by GitHub's API.

### Saving Output to a File

Use the `-o` flag to save usernames to a file (in addition to displaying them on stdout):

```bash
~/go-toolchain/go/bin/go run main.go -o stargazers.txt owner/repo
```

The file will contain one username per line:
```
user1
user2
user3
...
```

Example:
```bash
~/go-toolchain/go/bin/go run main.go -o golang-stars.txt golang/go
```

### With GitHub Token (Recommended)

For repositories with many stars, it's recommended to use a GitHub personal access token to increase the rate limit to 5000 requests per hour.

1. Create a GitHub personal access token:
   - Go to GitHub Settings > Developer settings > Personal access tokens
   - Generate a new token (no special scopes required for public repositories)

2. Set the token as an environment variable:
```bash
export GITHUB_TOKEN=your_token_here
```

3. Run the crawler:
```bash
~/go-toolchain/go/bin/go run main.go owner/repo
```

### Using the Built Binary

```bash
# Display to stdout only
./github-star-crawler owner/repo

# Display to stdout AND save to file
./github-star-crawler -o output.txt owner/repo
```

## Output Example

### Console Output (stdout)

```
Fetching stargazers for anthropics/anthropic-sdk-python...

Fetched page 1 (100 users)...
Fetched page 2 (100 users)...
Fetched page 3 (100 users)...
...
Found 2455 stargazers:

1. tom-doerr
2. ezzcodeezzlife
3. tthoraldson
...

Usernames written to: stargazers.txt
```

### File Output Format

When using the `-o` flag, the file contains one username per line:

```
tom-doerr
ezzcodeezzlife
tthoraldson
transitive-bullshit
tmabraham
...
```

## How It Works

The crawler uses the GitHub REST API endpoint:
```
GET https://api.github.com/repos/{owner}/{repo}/stargazers
```

- Fetches 100 stargazers per page (GitHub's maximum)
- Waits 1 second between requests to be respectful of rate limits
- Continues until all pages are fetched
- Displays progress and final count

## Rate Limits

- **Without token**: 60 requests/hour (can fetch ~6,000 stargazers)
- **With token**: 5,000 requests/hour (can fetch ~500,000 stargazers)

## Error Handling

The application handles various error conditions:
- Invalid repository format
- Network errors
- API rate limit errors
- Non-existent repositories

## Development

See [agent-log.md](agent-log.md) for development progress and technical details.

## License

This project is open source and available for educational purposes.
