// Package config loads runtime configuration from the environment.
package config

import (
	"os"
	"time"
)

// Config holds process configuration.
type Config struct {
	HTTPAddr string
	DSN      string
	Driver   string // "postgres" or "memory"
	StartAt  time.Time
}

// Load reads configuration with safe local defaults.
func Load() Config {
	c := Config{
		HTTPAddr: getenv("HTTP_ADDR", ":8080"),
		DSN:      getenv("DATABASE_DSN", "postgres://crack:crack@localhost:5432/crackstation?sslmode=disable"),
		Driver:   getenv("DRIVER", "postgres"),
	}
	if d := os.Getenv("START_NOW"); d != "" {
		if t, err := time.Parse(time.RFC3339, d); err == nil {
			c.StartAt = t
		}
	}
	return c
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
