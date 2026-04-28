package config

import "testing"

func TestBackupConfigSetDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.SetDefaults()

	if cfg.Backup.SourceType != "oss" {
		t.Fatalf("expected default source type oss, got %q", cfg.Backup.SourceType)
	}
	if cfg.Backup.SourceNamespace != "ems-au" {
		t.Fatalf("expected default source namespace ems-au, got %q", cfg.Backup.SourceNamespace)
	}
	if cfg.Backup.SourcePodName != "iotdb-datanode-0" {
		t.Fatalf("expected default source pod name iotdb-datanode-0, got %q", cfg.Backup.SourcePodName)
	}
	if cfg.Backup.SourceDataDir != "/iotdb/data/datanode" {
		t.Fatalf("unexpected default source data dir: %q", cfg.Backup.SourceDataDir)
	}
	if cfg.Backup.StagingDir != "/iotdb/data/restore_staging" {
		t.Fatalf("unexpected default staging dir: %q", cfg.Backup.StagingDir)
	}
	if cfg.Backup.ArchiveDir != "/tmp" {
		t.Fatalf("unexpected default archive dir: %q", cfg.Backup.ArchiveDir)
	}
}

func TestBackupConfigUsesClusterStream(t *testing.T) {
	tests := []struct {
		name string
		cfg  BackupConfig
		want bool
	}{
		{
			name: "cluster stream",
			cfg:  BackupConfig{SourceType: "cluster_stream"},
			want: true,
		},
		{
			name: "case insensitive",
			cfg:  BackupConfig{SourceType: "CLUSTER_STREAM"},
			want: true,
		},
		{
			name: "oss",
			cfg:  BackupConfig{SourceType: "oss"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.UsesClusterStream(); got != tt.want {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}
