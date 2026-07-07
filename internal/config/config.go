// Package config loads runtime configuration from environment variables.
package config

import "github.com/caarlos0/env/v11"

// Config holds env-sourced settings. Names for HTTP_PORT/DB_HOST/DB_USER/
// DB_PASSWORD/DB_NAME match ./PLAN.md §9. DBPort and DBTLS are additions
// needed to build the MySQL DSN (§9 does not enumerate them) and default to
// values matching production (port 3306, TLS required) so deployments that
// only set the documented five vars still behave correctly.
type Config struct {
	HTTPPort   string `env:"HTTP_PORT" envDefault:"8080"`
	DBHost     string `env:"DB_HOST,required"`
	DBPort     string `env:"DB_PORT" envDefault:"3306"`
	DBUser     string `env:"DB_USER,required"`
	DBPassword string `env:"DB_PASSWORD,required"`
	DBName     string `env:"DB_NAME,required"`
	// DBTLS toggles `tls=skip-verify` on the MySQL DSN (encrypt without
	// verifying the server cert — HeatWave's cert has no IP SAN, so full
	// verification fails). The flag exists so local/testcontainers MySQL
	// (no TLS listener) can still be exercised.
	DBTLS bool `env:"DB_TLS" envDefault:"true"`
}

func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
