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

	// JWKSURL, JWTIssuer, and JWTAudience configure JWT verification
	// (../PLAN.md §4.3). JWKSURL defaults to the in-cluster auth DNS name
	// (../PLAN.md §3 / ./PLAN.md §9); JWTAudience defaults to the fixed
	// contract value from ../PLAN.md §4.1. JWTIssuer has no default because
	// it is environment-specific (`auth.${DOMAIN}`) and must be set
	// explicitly per deployment.
	JWKSURL     string `env:"JWKS_URL" envDefault:"http://auth.auth.svc.cluster.local:3000/.well-known/jwks.json"`
	JWTIssuer   string `env:"JWT_ISSUER,required"`
	JWTAudience string `env:"JWT_AUDIENCE" envDefault:"core"`

	// NATSURL is the JetStream broker this service publishes schedule
	// domain events to (../PLAN.md §3/§7). Defaults to the in-cluster DNS
	// name; never hardcode a different value per-environment.
	NATSURL string `env:"NATS_URL" envDefault:"nats://nats.data.svc.cluster.local:4222"`
}

func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
