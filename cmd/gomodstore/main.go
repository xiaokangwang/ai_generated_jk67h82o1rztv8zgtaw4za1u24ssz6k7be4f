package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"gomodstore/internal/gomodstore"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "publish":
		err = runPublish(os.Args[2:])
	case "get":
		err = runGet(os.Args[2:])
	case "urls":
		err = runURLs(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "gomodstore: %v\n", err)
		os.Exit(1)
	}
}

func runPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	serverURL := fs.String("server", "http://localhost:8080", "gomodstore server URL")
	modulePrefix := fs.String("module-prefix", "", "module prefix, for example example.com/store")
	mirrorURL := fs.String("mirror", gomodstore.DefaultMirrorURL, "public GOPROXY mirror URL")
	timeout := fs.Duration("timeout", gomodstore.DefaultPublishTimeout, "overall publish timeout")
	poll := fs.Duration("poll", gomodstore.DefaultPollInterval, "mirror polling interval")
	jsonOutput := fs.Bool("json", false, "print publish result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *modulePrefix == "" {
		return fmt.Errorf("-module-prefix is required")
	}

	input, closeInput, err := openInput(fs.Args())
	if err != nil {
		return err
	}
	defer closeInput()

	result, err := gomodstore.Publish(context.Background(), gomodstore.PublishOptions{
		ServerURL:    *serverURL,
		ModulePrefix: *modulePrefix,
		MirrorURL:    *mirrorURL,
		Body:         input,
		HTTPClient:   http.DefaultClient,
		Timeout:      *timeout,
		PollInterval: *poll,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	fmt.Fprintln(os.Stdout, result.Module)
	return nil
}

func runGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	mirrorURL := fs.String("mirror", gomodstore.DefaultMirrorURL, "public GOPROXY mirror URL")
	outPath := fs.String("out", "-", "output file, or - for stdout")
	timeout := fs.Duration("timeout", 2*time.Minute, "download timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: gomodstore get [flags] <module>")
	}

	output, closeOutput, err := openOutput(*outPath)
	if err != nil {
		return err
	}
	defer closeOutput()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	_, err = gomodstore.Get(ctx, gomodstore.GetOptions{
		MirrorURL:  *mirrorURL,
		Module:     fs.Arg(0),
		Output:     output,
		HTTPClient: http.DefaultClient,
	})
	return err
}

func runURLs(args []string) error {
	fs := flag.NewFlagSet("urls", flag.ExitOnError)
	mirrorURL := fs.String("mirror", gomodstore.DefaultMirrorURL, "public GOPROXY mirror URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: gomodstore urls [flags] <module>")
	}
	urls, err := gomodstore.MirrorURLs(*mirrorURL, fs.Arg(0))
	if err != nil {
		return err
	}
	for _, u := range urls {
		fmt.Fprintln(os.Stdout, u)
	}
	return nil
}

func openInput(args []string) (io.Reader, func(), error) {
	if len(args) > 1 {
		return nil, nil, fmt.Errorf("usage: gomodstore publish [flags] [file]")
	}
	if len(args) == 0 || args[0] == "-" {
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, os.Stdin); err != nil {
			return nil, nil, err
		}
		return &buf, func() {}, nil
	}
	file, err := os.Open(args[0])
	if err != nil {
		return nil, nil, err
	}
	return file, func() { _ = file.Close() }, nil
}

func openOutput(path string) (io.Writer, func(), error) {
	if path == "-" {
		return os.Stdout, func() {}, nil
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return file, func() { _ = file.Close() }, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  gomodstore publish -module-prefix <prefix> [flags] [file]
  gomodstore get [flags] <module>
  gomodstore urls [flags] <module>`)
}
