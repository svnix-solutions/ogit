package config

import "time"

type Config struct {
	// Version of the application
	Version string

	// ConfigFile is the path to the configuration file
	ConfigFile string

	Server struct {
		// Port is the server port
		Port int
		// HttpLogs indicates if HTTP logs are enabled
		HttpLogs bool
		// PublicURL is the externally reachable base URL (e.g.
		// https://git.example.com), used in links sent by email
		PublicURL string
		// TrustedProxies are the IPs or CIDRs of reverse proxies whose
		// X-Forwarded-For and X-Forwarded-Proto headers are believed.
		TrustedProxies []string
	}

	// Webhooks: deliveries to private, loopback or link-local addresses are
	// refused unless the host, IP or CIDR is in AllowedHosts.
	Webhooks struct {
		AllowedHosts []string
		Timeout      time.Duration
	}

	// Mirrors: pull mirrors and imports of remote repositories.
	Mirrors struct {
		AllowedHosts    []string
		MinInterval     time.Duration
		DefaultInterval time.Duration
	}

	// Security: EncryptionKey encrypts secrets stored in the database
	// (helpers/secretbox).
	Security struct {
		EncryptionKey string
		// RequireTwoFactor forces two-factor authentication for every user
		// (otherwise the admin page decides).
		RequireTwoFactor bool
	}

	// Git: a push (its pack, over HTTP or SSH) is at most MaxPushSize
	// bytes; 0 means no limit.
	Git struct {
		MaxPushSize int64
	}

	// LFS: one Git LFS object is at most MaxObjectSize bytes.
	LFS struct {
		MaxObjectSize int64
	}

	// Packages: files (and container blobs) are at most MaxFileSize bytes.
	Packages struct {
		MaxFileSize int64
	}

	// Actions: artifacts are kept ArtifactRetentionDays (a workflow can
	// only shorten it) and are at most MaxArtifactSize bytes.
	Actions struct {
		ArtifactRetentionDays int
		MaxArtifactSize       int64
		// CacheSizeLimit bounds a repository's cache entries; the least
		// recently used go first.
		CacheSizeLimit int64
	}

	// Releases: uploaded assets are at most MaxAssetSize bytes.
	Releases struct {
		MaxAssetSize int64
	}

	// RateLimit rules are "N/window" (e.g. "10/15m"); "0" turns one off.
	RateLimit struct {
		Enabled      bool
		Store        string // memory or redis
		RedisURL     string
		LoginIP      string // failed logins and token checks per IP
		LoginAccount string // failed logins per account
		RegisterIP   string // registrations and verification emails per IP
		APIUser      string // API requests per signed-in user
		APIAnonymous string // API requests per IP without a session
		GitUser      string // git operations per user or deploy key
		GitAnonymous string // git operations per IP without credentials
	}

	// Logger is the configuration for the zerolog logger.
	// Level is the log level for the logger.
	// Pretty enables or disables pretty printing of logs (non JSON logs).
	Logger struct {
		Level  string
		Pretty bool
	}

	// Database is the configuration for the database connection.
	// Dialect is the database engine (sqlite, postgres, etc.).
	// DSN is the Data Source Name for the database connection.
	Database struct {
		Dialect string // Database engine (sqlite, postgres, etc.)
		DSN     string
	}

	Session struct {
		SecretKey         string
		ExpirationMinutes int
		Issuer            string
	}

	Registration struct {
		Enabled                  bool     // Enable or disable user registration
		RequireEmailVerification bool     // Require email verification for new registrations
		DomainWhitelist          []string // List of allowed domains for registration
		PasswordMinLength        int      // Minimum password length for registration
		PasswordComplexity       bool     // Require complex passwords (uppercase, lowercase, numbers, symbols)
		VerificationTTLHours     int      // How long an email verification link stays valid
		VerificationURLTemplate  string   // Link sent by email; {public_url} and {token} are replaced
	}

	// SSH is the git-over-SSH server.
	SSH struct {
		Enabled     bool
		Port        int
		HostKeyPath string // ed25519 host key, generated on first start if missing
	}

	// SMTP is the outgoing mail server, required for email verification.
	SMTP struct {
		Host     string
		Port     int
		Username string
		Password string
		From     string
		TLS      string // starttls, tls (implicit, usually port 465) or none
	}

	// StorageType is the type of storage to use (e.g., local, s3).
	Storage struct {
		Type string

		S3 struct {
			Bucket    string
			Endpoint  string
			AccessKey string
			SecretKey string
			Region    string

			// Cache holds pack blocks and indexes (immutable): memory
			// (default, MaxMB), redis (RedisURL, shared by servers) or none.
			Cache struct {
				Type     string
				MaxMB    int
				RedisURL string
			}
		}

		Local struct {
			Path string
		}
	}
}
