// Package config loads SaaS-side configuration from config.yaml + environment
// variables. Secrets must come from env vars only (config.yaml is committed
// without secrets, see config.yaml.example).
package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Config is the root SaaS configuration.
type Config struct {
	AppRole  string         `mapstructure:"app_role"` // "saas" / "lab" / "dev"
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Redis    RedisConfig    `mapstructure:"redis"`
	JWT      JWTConfig      `mapstructure:"jwt"`
}

type ServerConfig struct {
	Addr string `mapstructure:"addr"` // ":8080"
}

type DatabaseConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"` // env: EIGHTIQUANT_DATABASE_PASSWORD
	Name     string `mapstructure:"name"`     // typically "eighti_quant"
	SSLMode  string `mapstructure:"sslmode"`  // "disable" / "require"
}

// DSN returns the libpq connection string for this database config.
func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`     // "localhost:6379"
	Password string `mapstructure:"password"` // env: EIGHTIQUANT_REDIS_PASSWORD
	DB       int    `mapstructure:"db"`
}

type JWTConfig struct {
	Secret    string `mapstructure:"secret"` // env: EIGHTIQUANT_JWT_SECRET
	TTLHours  int    `mapstructure:"ttl_hours"`
	Issuer    string `mapstructure:"issuer"`
}

// Load reads config.yaml from configPath and overlays environment variables.
//
// Env var convention: EIGHTIQUANT_<SECTION>_<KEY> (uppercase, dot → underscore).
// Examples:
//   EIGHTIQUANT_DATABASE_PASSWORD=secret123
//   EIGHTIQUANT_JWT_SECRET=xyz
//   EIGHTIQUANT_APP_ROLE=dev
func Load(configPath string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")

	v.SetEnvPrefix("EIGHTIQUANT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("config: read %s: %w", configPath, err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	switch c.AppRole {
	case "saas", "lab", "dev":
	default:
		return fmt.Errorf("config: invalid app_role %q (must be saas/lab/dev)", c.AppRole)
	}
	if c.Database.Host == "" {
		return fmt.Errorf("config: database.host is required")
	}
	if c.Database.Name == "" {
		return fmt.Errorf("config: database.name is required")
	}
	if c.JWT.Secret == "" {
		return fmt.Errorf("config: jwt.secret is required (set via EIGHTIQUANT_JWT_SECRET)")
	}
	if c.JWT.TTLHours <= 0 {
		c.JWT.TTLHours = 24
	}
	return nil
}
