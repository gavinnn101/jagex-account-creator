package main

import (
	"errors"
	"flag"
	"fmt"
	"strconv"

	"jagex_account_creator/internal/config"
)

// cliOptions holds the parsed command line.
type cliOptions struct {
	configPath string
	mode       string
	out        string // the export file of export mode. The other modes ignore it
	listen     string // the API address of serve mode. The other modes ignore it
	// overrides holds one change to the loaded config for each config flag
	// that the command line gives.
	overrides []func(*config.Config)
}

// parseFlags parses args, the command line without the program name. A config
// flag that args does not give adds no override, so the config value stays.
// On an error the flag package has already printed the error and the usage.
func parseFlags(args []string) (cliOptions, error) {
	var opts cliOptions
	fs := flag.NewFlagSet("creator", flag.ContinueOnError)
	// Git ignores the default file, so the IMAP password that serve saves stays out of the tree.
	fs.StringVar(&opts.configPath, "config", "config.local.yaml", "path to config file. Copy the template config.yaml to make one")
	fs.StringVar(&opts.mode, "mode", "full",
		`run mode: "full" (create accounts), "challenge" (Turnstile debug only), "export" (write the stored accounts as JSONL) or "serve" (the local HTTP API)`)
	fs.StringVar(&opts.out, "out", "", "export mode: the path of the new JSONL file. The file must not exist")
	fs.StringVar(&opts.listen, "listen", "127.0.0.1:8080", "serve mode: the address of the HTTP API")
	fs.BoolFunc("headless", "run Chrome in headless mode (overrides config)",
		opts.boolOverride(func(cfg *config.Config, v bool) { cfg.Chrome.Headless = &v }))
	fs.BoolFunc("debug", "enable debug logging and screenshots (overrides config)",
		opts.boolOverride(func(cfg *config.Config, v bool) { cfg.Debug = v }))
	fs.Func("count", "number of accounts to create, -1 = unlimited (overrides config)", func(s string) error {
		n, err := strconv.Atoi(s)
		if err != nil {
			return strconvCause(err)
		}
		opts.overrides = append(opts.overrides, func(cfg *config.Config) { cfg.Concurrency.AccountCount = n })
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return cliOptions{}, fmt.Errorf("parsing flags: %w", err)
	}
	return opts, nil
}

// boolOverride returns a flag callback that parses a boolean and adds an
// override that gives it to set.
func (o *cliOptions) boolOverride(set func(cfg *config.Config, v bool)) func(string) error {
	return func(s string) error {
		v, err := strconv.ParseBool(s)
		if err != nil {
			return strconvCause(err)
		}
		o.overrides = append(o.overrides, func(cfg *config.Config) { set(cfg, v) })
		return nil
	}
}

// strconvCause returns the cause of a strconv error without the raw text,
// because the flag package already prints the text and the flag name.
func strconvCause(err error) error {
	if numErr, ok := errors.AsType[*strconv.NumError](err); ok {
		return numErr.Err
	}
	return err
}
