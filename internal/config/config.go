// Package config loads the YAML configuration and validates it.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"gopkg.in/yaml.v3"
)

// Load reads the config file with Read and validates the result with Validate.
func Load(path string, overrides ...func(*Config)) (*Config, error) {
	cfg, err := Read(path, overrides...)
	if err != nil {
		return nil, err
	}
	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// Read reads a YAML config file, fills the unset fields with defaults and
// applies overrides in order. It does not validate the result, so a caller can
// show a config that a run refuses. A key that Config does not define fails Read.
func Read(path string, overrides ...func(*Config)) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg, err := decode(data)
	if err != nil {
		return nil, err
	}

	applyDefaults(cfg)
	for _, override := range overrides {
		override(cfg)
	}
	return cfg, nil
}

// decode decodes YAML data into a new Config. An unknown key is an error, so a
// misspelled or removed key does not go silently unused. Empty data gives the
// zero Config, so Validate still names the required fields.
func decode(data []byte) (*Config, error) {
	cfg := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}
	return cfg, nil
}

// Defaults returns the config that Read returns for an empty file.
func Defaults() *Config {
	cfg := &Config{}
	applyDefaults(cfg)
	return cfg
}

// Save writes cfg to the YAML file at path. The file keeps its comments and its
// key order. A key that the file lacks goes at the end of its section. A missing
// file counts as empty. A file that Read refuses fails Save and stays unchanged.
// The new file replaces the old one in one rename, with mode 0600.
// Limitation: a save drops the blank lines between sections, the alignment of line comments and the quote style of a value. The comment text stays.
// Limitation: a CRLF file comes back with LF line endings, and its first save puts one blank line inside each multi-line comment block.
// Limitation: on Windows, a rename over a file that another process holds open fails and the old file stays. Mode bits do nothing on Windows. A symlink at path becomes a plain file.
func Save(path string, cfg *Config) error {
	old, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("reading config file: %w", err)
	}
	data, err := merged(old, cfg)
	if err != nil {
		return err
	}
	return replaceFile(path, data)
}

// merged returns the YAML text old with the values of cfg. The strict decode
// refuses the text that Read refuses, so a save never writes past such an error.
func merged(old []byte, cfg *Config) ([]byte, error) {
	if _, err := decode(old); err != nil {
		return nil, err
	}
	var doc, src yaml.Node
	// old passed the decode and a Config always encodes. The join still returns an error that a yaml.v3 change could bring.
	err := errors.Join(yaml.Unmarshal(old, &doc), src.Encode(cfg))
	var head []byte
	root := &doc
	if doc.Kind == 0 {
		// The parser keeps no node for a file of only comments, so the old text goes first as it is.
		head = old
		if len(old) > 0 && !bytes.HasSuffix(old, []byte("\n")) {
			head = append(head, '\n')
		}
		root = &src
	} else {
		mergeNode(doc.Content[0], &src)
	}
	buf := bytes.NewBuffer(head)
	enc := yaml.NewEncoder(buf)
	enc.SetIndent(2)
	err = errors.Join(err, enc.Encode(root), enc.Close())
	return buf.Bytes(), err
}

// mergeNode sets dst to src. Two mappings merge by key, and a key that dst
// lacks goes at its end. Any other value replaces dst and keeps the comments of dst.
func mergeNode(dst, src *yaml.Node) {
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		head, line, foot := dst.HeadComment, dst.LineComment, dst.FootComment
		*dst = *src
		dst.HeadComment, dst.LineComment, dst.FootComment = head, line, foot
		return
	}
	for pair := range slices.Chunk(src.Content, 2) {
		if old := mappingValue(dst, pair[0].Value); old != nil {
			mergeNode(old, pair[1])
		} else {
			dst.Content = append(dst.Content, pair...)
		}
	}
}

// mappingValue returns the value node of key in the mapping m, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for pair := range slices.Chunk(m.Content, 2) {
		if pair[0].Value == key {
			return pair[1]
		}
	}
	return nil
}

// replaceFile writes data to a new temp file beside path and renames it to
// path, so a reader sees the old file or the new one, never a part.
func replaceFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating the temp config file: %w", err)
	}
	if err := commitTemp(f, path, data); err != nil {
		return errors.Join(err, os.Remove(f.Name()))
	}
	return nil
}

// commitTemp writes data to f, closes f and renames it to path. f is closed
// on every return, so the caller can remove it.
func commitTemp(f *os.File, path string, data []byte) error {
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replacing config file: %w", err)
	}
	return nil
}

func applyDefaults(cfg *Config) {
	if cfg.Chrome.Timeout == 0 {
		cfg.Chrome.Timeout = 60 * time.Second
	}
	if cfg.Chrome.Headless == nil {
		cfg.Chrome.Headless = new(true)
	}
	if cfg.Account.EnableTOTP == nil {
		cfg.Account.EnableTOTP = new(true)
	}
	if cfg.Account.PasswordLength == 0 {
		cfg.Account.PasswordLength = 16
	}
	if cfg.Database == "" {
		cfg.Database = "creator.db"
	}
	if cfg.Concurrency.MaxWorkers == 0 {
		cfg.Concurrency.MaxWorkers = 1
	}
	if cfg.Concurrency.MaxBrowsers == 0 {
		cfg.Concurrency.MaxBrowsers = cfg.Concurrency.MaxWorkers
	}
	if cfg.Concurrency.AccountCount == 0 {
		cfg.Concurrency.AccountCount = 1
	}
	if cfg.Concurrency.AccountTimeout == 0 {
		cfg.Concurrency.AccountTimeout = 5 * time.Minute
	}
	if cfg.Email.IMAPPort == 0 {
		cfg.Email.IMAPPort = 993
	}
}

// Validate checks cfg for invalid values. It fills no default, so a caller
// passes a config that Read returned or one that holds every value. The result
// joins one error for each invalid value.
func Validate(cfg *Config) error {
	var errs []error

	if cfg.Database == "" {
		errs = append(errs, errors.New("database must not be empty"))
	}

	// Chrome
	if cfg.Chrome.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("chrome.timeout must be positive, got %v", cfg.Chrome.Timeout))
	}

	// Account
	if cfg.Account.PasswordLength < 8 {
		errs = append(errs, fmt.Errorf("account.password_length must be at least 8, got %d", cfg.Account.PasswordLength))
	}

	errs = append(errs, validateConcurrency(cfg.Concurrency)...)

	// Email
	if cfg.Email.IMAPPort < 1 || cfg.Email.IMAPPort > 65535 {
		errs = append(errs, fmt.Errorf("email.imap_port must be between 1 and 65535, got %d", cfg.Email.IMAPPort))
	}
	if cfg.Email.IMAPHost == "" {
		errs = append(errs, errors.New("email.imap_host is required"))
	}
	if cfg.Email.IMAPUser == "" {
		errs = append(errs, errors.New("email.imap_user is required"))
	}
	if cfg.Email.IMAPPass == "" {
		errs = append(errs, errors.New("email.imap_pass is required"))
	}
	if cfg.Email.IMAPDomain == "" {
		errs = append(errs, errors.New("email.imap_domain is required"))
	}

	return errors.Join(errs...)
}

// validateConcurrency checks the concurrency section. It skips the max_browsers
// checks when max_workers is invalid, because that error names the real cause.
func validateConcurrency(c ConcurrencyConfig) []error {
	var errs []error
	switch {
	case c.MaxWorkers < 1:
		errs = append(errs, fmt.Errorf("concurrency.max_workers must be at least 1, got %d", c.MaxWorkers))
	case c.MaxBrowsers < 1:
		errs = append(errs, fmt.Errorf("concurrency.max_browsers must be at least 1, got %d", c.MaxBrowsers))
	case c.MaxBrowsers > c.MaxWorkers:
		errs = append(errs, fmt.Errorf("concurrency.max_browsers must not exceed concurrency.max_workers (%d), got %d", c.MaxWorkers, c.MaxBrowsers))
	}
	if c.AccountCount == 0 || c.AccountCount < -1 {
		errs = append(errs, fmt.Errorf("concurrency.account_count must be positive or -1 (unlimited), got %d", c.AccountCount))
	}
	if c.AccountTimeout <= 0 {
		errs = append(errs, fmt.Errorf("concurrency.account_timeout must be positive, got %v", c.AccountTimeout))
	}
	return errs
}
