package main

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config is everything the process reads from its environment. Every variable has a default that
// works for the Docker command in the README, so `go run .` against that container needs none.
type Config struct {
	Addr               string         // CHAT_ADDR, ":8080"
	DatabaseURL        string         // CHAT_DATABASE_URL
	TrustedProxies     []netip.Prefix // CHAT_TRUSTED_PROXIES, comma separated CIDRs; none by default
	PublicURL          string         // CHAT_PUBLIC_URL, where clients reach the API, "https://example.com/chat"; none by default
	RateLimitPerMinute int            // CHAT_RATE_LIMIT, 600 requests a minute for each client
	ShutdownDelay      time.Duration  // CHAT_SHUTDOWN_DELAY, 2s: readiness fails and traffic is still served
	ShutdownTimeout    time.Duration  // CHAT_SHUTDOWN_TIMEOUT, 25s: the most Shutdown waits for requests
}

// LoadConfig reads the configuration through getenv (os.Getenv in main), and says every variable
// it could not use, not only the first.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:               ":8080",
		DatabaseURL:        "postgres://postgres:chat@127.0.0.1:5432/postgres",
		RateLimitPerMinute: 600,
		ShutdownDelay:      2 * time.Second,
		ShutdownTimeout:    25 * time.Second,
	}
	var errs []error
	if v := getenv("CHAT_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := getenv("CHAT_DATABASE_URL"); v != "" {
		cfg.DatabaseURL = v
	}
	if v := getenv("CHAT_TRUSTED_PROXIES"); v != "" {
		for _, p := range strings.Split(v, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(p))
			if err != nil {
				errs = append(errs, fmt.Errorf("CHAT_TRUSTED_PROXIES: %w", err))
				continue
			}
			cfg.TrustedProxies = append(cfg.TrustedProxies, prefix)
		}
	}
	if v := getenv("CHAT_PUBLIC_URL"); v != "" {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(v, "/") {
			errs = append(errs, fmt.Errorf("CHAT_PUBLIC_URL: %q is not an http or https URL with no trailing slash, such as https://example.com/chat", v))
		} else {
			cfg.PublicURL = v
		}
	}
	if v := getenv("CHAT_RATE_LIMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("CHAT_RATE_LIMIT: %q is not a positive integer", v))
		} else {
			cfg.RateLimitPerMinute = n
		}
	}
	for _, d := range []struct {
		name string
		to   *time.Duration
	}{{"CHAT_SHUTDOWN_DELAY", &cfg.ShutdownDelay}, {"CHAT_SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout}} {
		if v := getenv(d.name); v != "" {
			dur, err := time.ParseDuration(v)
			if err != nil || dur < 0 {
				errs = append(errs, fmt.Errorf("%s: %q is not a duration such as 5s", d.name, v))
			} else {
				*d.to = dur
			}
		}
	}
	return cfg, errors.Join(errs...)
}

// Prefix is the path of PublicURL, the prefix a proxy in front strips before the app sees a
// request: "/chat" for https://example.com/chat, "" without a PublicURL.
func (c Config) Prefix() string {
	u, err := url.Parse(c.PublicURL)
	if err != nil {
		return ""
	}
	return u.EscapedPath()
}
