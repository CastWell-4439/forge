// Package database implements the Database Worker for Forge workflows.
// It provides read-only database access (PG SELECT, Redis GET/KEYS).
package database

import (
	"fmt"
	"os"
)

// PGConfig holds PostgreSQL connection parameters.
type PGConfig struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	DB          string `yaml:"db"`
	User        string `yaml:"user"`
	Password    string `yaml:"password"`     // direct value (not recommended)
	PasswordEnv string `yaml:"password_env"` // env var name for password
}

// DSN returns the PostgreSQL connection string.
func (c *PGConfig) DSN() string {
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

// RedisConfig holds Redis connection parameters.
type RedisConfig struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	DB          int    `yaml:"db"`
	Password    string `yaml:"password"`
	PasswordEnv string `yaml:"password_env"`
}

// Addr returns host:port for Redis.
func (c *RedisConfig) Addr() string {
	port := c.Port
	if port == 0 {
		port = 6379
	}
	return fmt.Sprintf("%s:%d", c.Host, port)
}

// GetPassword resolves the Redis password from env or direct value.
func (c *RedisConfig) GetPassword() string {
	if c.PasswordEnv != "" {
		if v := os.Getenv(c.PasswordEnv); v != "" {
			return v
		}
	}
	return c.Password
}

// Config holds all database configurations for a project.
type Config struct {
	Postgres *PGConfig    `yaml:"postgres"`
	Redis    *RedisConfig `yaml:"redis"`
}
