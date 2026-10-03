// Package database implements the Database Worker for Forge workflows.
// It provides read-only database access (PG SELECT).
package database

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Environment variables that configure the database worker. FORGE_PG_DSN
// (a full connection string) wins; otherwise the individual FORGE_PG_*
// values are assembled. Filling either in is enough to make the worker
// runnable — there is no third step.
const (
	EnvPGDSN         = "FORGE_PG_DSN"
	EnvPGHost        = "FORGE_PG_HOST"
	EnvPGPort        = "FORGE_PG_PORT"
	EnvPGDB          = "FORGE_PG_DB"
	EnvPGUser        = "FORGE_PG_USER"
	EnvPGPassword    = "FORGE_PG_PASSWORD"
	EnvPGPasswordEnv = "FORGE_PG_PASSWORD_ENV"
)

// PGConfig holds PostgreSQL connection parameters.
type PGConfig struct {
	// ConnectionString, when set, is used verbatim and overrides every
	// field below. It exists so a full DSN (URL form, extra parameters and
	// all) round-trips without being decomposed and rebuilt.
	ConnectionString string `yaml:"dsn,omitempty"`

	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	DB          string `yaml:"db"`
	User        string `yaml:"user"`
	Password    string `yaml:"password"`     // direct value (not recommended)
	PasswordEnv string `yaml:"password_env"` // env var name for password
}

// DSN returns the PostgreSQL connection string.
func (c *PGConfig) DSN() string {
	if c.ConnectionString != "" {
		return c.ConnectionString
	}
	password := c.Password
	if c.PasswordEnv != "" {
		if v := os.Getenv(c.PasswordEnv); v != "" {
			password = v
		}
	}
	port := c.Port
	if port == 0 {
		port = 5432
	}
	return fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=disable",
		c.Host, port, c.DB, c.User, password)
}

// ConfigFromEnv builds the worker config from the environment. It returns
// nil when nothing is configured, which callers report as "not configured"
// instead of letting the first query fail on an empty connection string.
func ConfigFromEnv() *Config {
	if dsn := strings.TrimSpace(os.Getenv(EnvPGDSN)); dsn != "" {
		return &Config{Postgres: &PGConfig{ConnectionString: dsn}}
	}
	host := strings.TrimSpace(os.Getenv(EnvPGHost))
	if host == "" {
		return nil
	}
	pg := &PGConfig{
		Host:        host,
		DB:          os.Getenv(EnvPGDB),
		User:        os.Getenv(EnvPGUser),
		Password:    os.Getenv(EnvPGPassword),
		PasswordEnv: os.Getenv(EnvPGPasswordEnv),
	}
	// A malformed port falls through as 0, which DSN() renders as the
	// default 5432 — same conservative default as an unset port.
	if port, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvPGPort))); err == nil {
		pg.Port = port
	}
	return &Config{Postgres: pg}
}

// Config holds all database configurations for a project.
type Config struct {
	Postgres *PGConfig `yaml:"postgres"`
}
