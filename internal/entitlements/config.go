package entitlements

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout          = 5 * time.Second
	defaultMaxResponseBytes = 16 << 10
	defaultPositiveCacheTTL = 30 * time.Second
	defaultNegativeCacheTTL = 5 * time.Second
	defaultCacheCapacity    = 256
	minTimeout              = time.Second
	maxTimeout              = 30 * time.Second
	minResponseBytes        = 1 << 10
	maxResponseBytes        = 64 << 10
	minCacheTTL             = time.Second
	maxCacheTTL             = 5 * time.Minute
	maxCacheCapacity        = 1024
)

// Config configures the Dev Health service entitlement boundary. It excludes
// the token value: New reads that secret only from TokenFile.
type Config struct {
	BaseURL               *url.URL
	TokenFile             string
	Timeout               time.Duration
	MaxResponseBytes      int64
	ProxyURL              *url.URL
	CACertPath            string
	AllowInsecureLoopback bool
	PositiveCacheTTL      time.Duration
	NegativeCacheTTL      time.Duration
	CacheCapacity         int
}

func (c Config) withDefaults() Config {
	if c.Timeout == 0 {
		c.Timeout = defaultTimeout
	}
	if c.MaxResponseBytes == 0 {
		c.MaxResponseBytes = defaultMaxResponseBytes
	}
	if c.PositiveCacheTTL == 0 {
		c.PositiveCacheTTL = defaultPositiveCacheTTL
	}
	if c.NegativeCacheTTL == 0 {
		c.NegativeCacheTTL = defaultNegativeCacheTTL
	}
	if c.CacheCapacity == 0 {
		c.CacheCapacity = defaultCacheCapacity
	}
	return c
}

func (c Config) validate() error {
	if c.BaseURL == nil || c.BaseURL.Scheme == "" || c.BaseURL.Host == "" {
		return errors.New("Dev Health entitlement URL is required")
	}
	if c.BaseURL.User != nil || c.BaseURL.RawQuery != "" || c.BaseURL.Fragment != "" || c.BaseURL.Path != "" {
		return errors.New("Dev Health entitlement URL must be an origin")
	}
	if c.BaseURL.Scheme != "https" && !(c.AllowInsecureLoopback && c.BaseURL.Scheme == "http" && isLoopback(c.BaseURL.Hostname())) {
		return errors.New("Dev Health entitlement URL must use HTTPS")
	}
	if strings.TrimSpace(c.TokenFile) == "" {
		return errors.New("Dev Health entitlement token file is required")
	}
	if c.Timeout < minTimeout || c.Timeout > maxTimeout {
		return fmt.Errorf("Dev Health entitlement timeout must be between %s and %s", minTimeout, maxTimeout)
	}
	if c.MaxResponseBytes < minResponseBytes || c.MaxResponseBytes > maxResponseBytes {
		return fmt.Errorf("Dev Health entitlement response limit must be between %d and %d bytes", minResponseBytes, maxResponseBytes)
	}
	if c.PositiveCacheTTL < minCacheTTL || c.PositiveCacheTTL > maxCacheTTL || c.NegativeCacheTTL < minCacheTTL || c.NegativeCacheTTL > maxCacheTTL {
		return fmt.Errorf("Dev Health entitlement cache TTL must be between %s and %s", minCacheTTL, maxCacheTTL)
	}
	if c.CacheCapacity < 1 || c.CacheCapacity > maxCacheCapacity {
		return fmt.Errorf("Dev Health entitlement cache capacity must be between 1 and %d", maxCacheCapacity)
	}
	if c.ProxyURL != nil && (c.ProxyURL.Scheme != "http" && c.ProxyURL.Scheme != "https" || c.ProxyURL.Host == "" || c.ProxyURL.User != nil) {
		return errors.New("Dev Health entitlement proxy must be an HTTP(S) URL without credentials")
	}
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
