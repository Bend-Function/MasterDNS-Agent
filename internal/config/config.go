package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const maxConcurrency = 100

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Config struct {
	ServerURL           string   `json:"serverUrl"`
	ProbeID             string   `json:"probeId"`
	TokenFile           string   `json:"tokenFile"`
	StateDir            string   `json:"stateDir"`
	MaxConcurrency      int      `json:"maxConcurrency"`
	AllowIPv4           bool     `json:"allowIpv4"`
	AllowIPv6           bool     `json:"allowIpv6"`
	AllowedPrivateCIDRs []string `json:"allowedPrivateCidrs"`
}

func Load(path string) (Config, error) {
	return load(path, false)
}

// LoadForTesting permits loopback HTTP servers while retaining all other checks.
func LoadForTesting(path string) (Config, error) {
	return load(path, true)
}

func load(path string, allowTestHTTP bool) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer f.Close()

	var cfg Config
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := requireEOF(decoder); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := cfg.validate(allowTestHTTP); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return cfg, nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (cfg Config) validate(allowTestHTTP bool) error {
	u, err := url.Parse(cfg.ServerURL)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("serverUrl must be an absolute URL without credentials")
	}
	if u.Scheme != "https" {
		if !allowTestHTTP || u.Scheme != "http" || !isLoopbackHost(u.Hostname()) {
			return errors.New("serverUrl must use HTTPS")
		}
	}
	if !uuidPattern.MatchString(cfg.ProbeID) {
		return errors.New("probeId must be a UUID")
	}
	if cfg.TokenFile == "" {
		return errors.New("tokenFile is required")
	}
	if cfg.StateDir == "" {
		return errors.New("stateDir is required")
	}
	if cfg.MaxConcurrency < 1 || cfg.MaxConcurrency > maxConcurrency {
		return fmt.Errorf("maxConcurrency must be between 1 and %d", maxConcurrency)
	}
	if !cfg.AllowIPv4 && !cfg.AllowIPv6 {
		return errors.New("at least one address family must be enabled")
	}
	for _, raw := range cfg.AllowedPrivateCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix != prefix.Masked() {
			return fmt.Errorf("allowedPrivateCidrs contains invalid prefix %q", raw)
		}
	}
	return validateTokenFile(cfg.TokenFile)
}

func isLoopbackHost(host string) bool {
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

func validateTokenFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat tokenFile: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("tokenFile must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return errors.New("tokenFile must not be accessible by group or other users")
	}
	if runtime.GOOS == "windows" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("find current user directory: %w", err)
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve tokenFile: %w", err)
		}
		relative, err := filepath.Rel(home, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("tokenFile must be inside the current user's directory on Windows")
		}
	}
	return nil
}
