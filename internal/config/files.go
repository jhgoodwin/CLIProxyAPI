package config

// FilesConfig configures the shared, durable image store behind /v1/files.
type FilesConfig struct {
	Enabled                bool   `yaml:"enabled" json:"enabled"`
	StorageDir             string `yaml:"storage-dir" json:"storage-dir"`
	MaxFiles               int    `yaml:"max-files" json:"max-files"`
	MaxStorageBytes        int64  `yaml:"max-storage-bytes" json:"max-storage-bytes"`
	CleanupIntervalSeconds int    `yaml:"cleanup-interval-seconds" json:"cleanup-interval-seconds"`
}
