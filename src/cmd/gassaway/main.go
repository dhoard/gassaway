// Copyright (c) 2026-present Douglas Hoard. All rights reserved.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Command gassaway is the memory server: one long-lived process serving
// Streamable HTTP, with one knowledge graph per project.
//
// It starts in the foreground and logs to stderr, so it composes with a terminal,
// a systemd unit, or nohup. The deployment facts are stated on the command line
// and have no defaults: --host, --port and --project are required, and the data
// directory is created when it does not exist. Every option is written with two
// leading dashes, and a running instance reports its version through GET /healthz.
//
// Exit codes: 0 success, 2 usage error (a missing or malformed option, nothing
// started and nothing created), 1 a storage, bind or shutdown failure.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dhoard/gassaway/src/buildinfo"
	"github.com/dhoard/gassaway/src/config"
	"github.com/dhoard/gassaway/src/server"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// parseInvocation parses the command line. code is not negative when parsing
// decided the outcome on its own (a help request or a malformed option), and the
// FlagSet comes back so a caller that rejects the options can still print usage.
func parseInvocation(args []string) (opts config.Options, fs *flag.FlagSet, code int) {
	fs = flag.NewFlagSet("gassaway", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.Host, "host", "", "")
	fs.StringVar(&opts.Port, "port", "", "")
	fs.StringVar(&opts.ProjectsDir, "project", "", "")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `gassaway — memory server, one knowledge graph per project

Usage:
  gassaway --host <hostname|ip address> --port <port> --project <directory>
  gassaway --help

--host, --port and --project are required and have no defaults: the server binds
the address it is given rather than an address it invents, and it stores projects
in the directory it is given, creating that directory (and any missing parent)
when it does not exist.

  --host     hostname or IP address to bind
  --port     TCP port to bind, %d-%d
  --project  directory holding one subdirectory per project, created if missing
  --help     this text

Options are written with two leading dashes. The version is reported by GET
/healthz on a running instance.
`, config.MinPort, config.MaxPort)
	}
	if !requireLongOptions(fs, args) {
		return opts, fs, 2
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return opts, fs, 0
		}
		return opts, fs, 2
	}
	return opts, fs, -1
}

// requireLongOptions rejects the single-dash spelling the flag package also
// accepts. The format is part of the documented contract, and an option copied
// from an old script should fail loudly rather than quietly work differently
// from what the usage text says.
func requireLongOptions(fs *flag.FlagSet, args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if len(arg) > 1 && arg[0] == '-' && arg[1] != '-' {
			fmt.Fprintf(fs.Output(), "gassaway: %q: options are written --name\n", arg)
			fs.Usage()
			return false
		}
	}
	return true
}

func run(args []string) int {
	opts, fs, code := parseInvocation(args)
	if code >= 0 {
		return code
	}

	cfg, err := config.Parse(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gassaway: %v\n", err)
		fs.Usage()
		return 2
	}

	return serve(cfg)
}

// serve creates the data directory, opens the listener and runs until a signal or
// a serve failure, returning the process exit code.
func serve(cfg config.Config) int {
	// Create the data directory before the listener exists: a directory that
	// cannot be used is a start failure an operator can see, not a failed request
	// discovered hours later.
	if err := cfg.EnsureProjectDir(); err != nil {
		fmt.Fprintf(os.Stderr, "gassaway: %v\n", err)
		return 1
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	srv := server.New(cfg, logger)

	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		fmt.Fprintf(os.Stderr, "gassaway: listen on %s: %v\n", cfg.Addr(), err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "gassaway %s listening on %s, projects in %s\n",
		buildinfo.Version, ln.Addr().String(), cfg.ProjectsDir)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		if err != nil {
			fmt.Fprintf(os.Stderr, "gassaway: %v\n", err)
			return 1
		}
		return 0
	case <-signals:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "gassaway: shutdown: %v\n", err)
			return 1
		}
		<-serveErr
		return 0
	}
}
