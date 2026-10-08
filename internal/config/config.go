package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Required reads a required environment variable.
// Startup fails clearly if configuration is missing.
func Required(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf(
			"required environment variable %s is missing",
			name,
		)
	}

	return value, nil
}

// PositiveDuration reads a duration such as "2s" or "200ms".
func PositiveDuration(name string) (time.Duration, error) {
	value, err := Required(name)
	if err != nil {
		return 0, err
	}

	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf(
			"%s must be a positive duration such as 2s or 200ms",
			name,
		)
	}

	return duration, nil
}
