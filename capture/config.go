package capture

// CaptureConfig controls host-independent capture and delivery. Hosts own the
// configuration encoding, plugin registration and management authentication.
type CaptureConfig struct {
	Enabled              bool   `yaml:"enabled"`
	Endpoint             string `yaml:"endpoint"`
	QueueSize            int    `yaml:"queue_size"`
	QueueMaxBytes        int64  `yaml:"queue_max_bytes"`
	SpoolDir             string `yaml:"spool_dir"`
	SpoolMaxBytes        int64  `yaml:"spool_max_bytes"`
	SpoolMaxEntries      int    `yaml:"spool_max_entries"`
	MaxBodyBytes         int    `yaml:"max_body_bytes"`
	Timeout              string `yaml:"timeout"`
	StoreUpstreamRequest bool   `yaml:"store_upstream_request"`
}

func DefaultCaptureConfig() CaptureConfig {
	return CaptureConfig{Enabled: true, QueueSize: 2048, QueueMaxBytes: 256 << 20, MaxBodyBytes: 64 << 20, Timeout: "5s", SpoolMaxBytes: 2 << 30, SpoolMaxEntries: 10000}
}
