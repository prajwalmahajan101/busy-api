package config

import "context"

// loadCloudSecrets is a seam for fetching secrets from a cloud secrets manager
// (e.g. AWS Secrets Manager, GCP Secret Manager) and injecting them as env vars
// via os.Setenv before env.Parse runs.
//
// The no-op implementation here is intentional for local development. In a cloud
// environment, replace the body with the appropriate SDK calls. See ADR-0002.
func loadCloudSecrets(_ context.Context) error {
	return nil
}
