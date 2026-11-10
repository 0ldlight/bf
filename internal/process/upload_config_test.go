package process

import (
	"errors"
	"testing"
)

func TestArtifactsConfigRequiresExplicitUploadSettings(t *testing.T) {
	root := t.TempDir()
	load := func(mode string, fill bool) (Config, error) {
		doc := ppDownloadJSON(t, root, func(fields map[string]any) {
			if fill {
				fields["upload_writer_dsn"] = ppValidDSN
				fields["upload_reservation_dsn"] = ppValidDSN
				fields["upload_public_origin"] = "https://example.test"
				fields["upload_max_body_bytes"] = 1024
			}
		})
		path := ppWritePerm(t, doc, 0o600)
		return LoadConfig(ppDownloadEnv(path, map[string]string{"BINFLOW_MODE": mode}))
	}
	if _, err := load(ModeArtifacts, false); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("artifacts accepted download-only config: %v", err)
	}
	if _, err := load(ModeDownload, true); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("download accepted upload settings: %v", err)
	}
	cfg, err := load(ModeArtifacts, true)
	if err != nil || cfg.Mode != ModeArtifacts || cfg.Upload.MaxBodyBytes != 1024 {
		t.Fatalf("artifacts config = %v: %v", cfg, err)
	}
}
