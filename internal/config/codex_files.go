package config

// CodexFilesPrototypeConfig bounds the disposable HTTP files prototype.
type CodexFilesPrototypeConfig struct {
	Enabled    bool  `yaml:"enabled" json:"enabled"`
	MaxBytes   int64 `yaml:"max-bytes" json:"max-bytes"`
	MaxFiles   int   `yaml:"max-files" json:"max-files"`
	TTLSeconds int   `yaml:"ttl-seconds" json:"ttl-seconds"`
}

func (c CodexFilesPrototypeConfig) Limits() (int64, int, int) {
	size, count, ttl := c.MaxBytes, c.MaxFiles, c.TTLSeconds
	if size <= 0 || size > 20*1024*1024 {
		size = 5 * 1024 * 1024
	}
	if count <= 0 || count > 1024 {
		count = 128
	}
	if ttl <= 0 || ttl > 86400 {
		ttl = 3600
	}
	return size, count, ttl
}
