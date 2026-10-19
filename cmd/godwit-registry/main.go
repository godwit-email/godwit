// Command godwit-registry checks and signs a Godwit plugin registry.json
// (docs/registry/README.md). The plugins repository's CI runs it with
// `go run github.com/godwit-email/godwit/cmd/godwit-registry@main`.
//
//	godwit-registry check [--fetch] registry.json   validate; --fetch also downloads every file (artifacts and assets) and checks its size and sha256
//	godwit-registry sign registry.json              write registry.json.sig with the key in GODWIT_REGISTRY_KEY
//	godwit-registry keygen                          print a new key pair
//	godwit-registry dev [--addr host:port] spec.json  serve a local registry for development builds (dev.go)
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"time"

	"github.com/godwit-email/godwit/core/plugins"
)

// client downloads artifacts for check --fetch (tests replace it). No
// overall timeout: a model is gigabytes; checkFile gives up on a stalled one.
var client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: time.Minute}}

// fetchStall is how long checkFile waits for the next bytes of a file.
var fetchStall = time.Minute

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: godwit-registry check [--fetch] registry.json | sign registry.json | keygen | dev [--addr host:port] spec.json")
		return 2
	}
	var err error
	switch args[0] {
	case "check":
		fs := flag.NewFlagSet("check", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fetch := fs.Bool("fetch", false, "download every file and check it")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 1 {
			return 2
		}
		err = check(fs.Arg(0), *fetch, stdout)
	case "sign":
		if len(args) != 2 {
			return 2
		}
		err = sign(args[1])
	case "dev":
		fs := flag.NewFlagSet("dev", flag.ContinueOnError)
		fs.SetOutput(stderr)
		addr := fs.String("addr", "127.0.0.1:8743", "where to serve; keep it the same so installed plugins stay valid")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 1 {
			return 2
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		err = devRegistry(ctx, fs.Arg(0), *addr, stdout)
	case "keygen":
		pub, priv, kerr := ed25519.GenerateKey(nil)
		if kerr != nil {
			err = kerr
			break
		}
		fmt.Fprintln(stdout, "public:", base64.StdEncoding.EncodeToString(pub))
		fmt.Fprintln(stdout, "private:", base64.StdEncoding.EncodeToString(priv.Seed()))
	default:
		fmt.Fprintf(stderr, "godwit-registry: unknown command %q\n", args[0])
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "godwit-registry:", err)
		return 1
	}
	return 0
}

func check(path string, fetch bool, stdout io.Writer) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	reg, err := plugins.CheckRegistry(b)
	if err != nil {
		return err
	}
	if !fetch {
		fmt.Fprintf(stdout, "%d plugins, valid\n", len(reg.Plugins))
		return nil
	}
	var errs []error
	for _, p := range reg.Plugins {
		for _, rel := range p.Releases {
			keys := make([]string, 0, len(rel.Artifacts))
			for k := range rel.Artifacts {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				if err := checkFile(rel.Artifacts[k]); err != nil {
					errs = append(errs, fmt.Errorf("%s %s %s: %w", p.Name, rel.Version, k, err))
				} else {
					fmt.Fprintf(stdout, "%s %s %s: ok\n", p.Name, rel.Version, k)
				}
			}
			for _, a := range rel.Assets {
				for _, v := range a.Variants {
					for _, f := range v.Files {
						where := fmt.Sprintf("%s %s %s %s %s", p.Name, rel.Version, a.Name, v.ID, f.Name)
						if err := checkFile(plugins.Artifact{URL: f.URL, SHA256: f.SHA256, Size: f.Size}); err != nil {
							errs = append(errs, fmt.Errorf("%s: %w", where, err))
						} else {
							fmt.Fprintf(stdout, "%s: ok\n", where)
						}
					}
				}
			}
		}
	}
	return errors.Join(errs...)
}

func checkFile(a plugins.Artifact) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stall := time.AfterFunc(fetchStall, cancel)
	defer stall.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", a.URL, resp.Status)
	}
	h := sha256.New()
	n, err := io.Copy(h, stallReader{io.LimitReader(resp.Body, a.Size+1), stall})
	if ctx.Err() != nil {
		return fmt.Errorf("%s sent nothing for %s", a.URL, fetchStall)
	}
	if err != nil {
		return err
	}
	if n != a.Size {
		return fmt.Errorf("size is not %d", a.Size)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != a.SHA256 {
		return fmt.Errorf("sha256 is %s, not %s", sum, a.SHA256)
	}
	return nil
}

func sign(path string) error {
	seed, err := base64.StdEncoding.DecodeString(os.Getenv("GODWIT_REGISTRY_KEY"))
	if err != nil || len(seed) != ed25519.SeedSize {
		return errors.New("GODWIT_REGISTRY_KEY must hold the base64 private key that keygen printed")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := plugins.CheckRegistry(b); err != nil {
		return err // Godwit would reject it: never sign it
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), b)
	return os.WriteFile(path+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644)
}

// stallReader restarts its timer on every read that returns bytes.
type stallReader struct {
	r     io.Reader
	timer *time.Timer
}

func (s stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.timer.Reset(fetchStall)
	}
	return n, err
}
