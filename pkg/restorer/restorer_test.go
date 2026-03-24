package restorer

import (
	"errors"
	"testing"

	"github.com/vnnox/iotdb-restore-tool/pkg/config"
)

func TestParseCLITable(t *testing.T) {
	output := `
+------------+----+
|    Database| TTL|
+------------+----+
|root.emsplus|null|
| root.energy|null|
+------------+----+
Total line number = 2
`

	rows := parseCLITable(output)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	if rows[0]["Database"] != "root.emsplus" {
		t.Fatalf("unexpected first database: %q", rows[0]["Database"])
	}

	if rows[1]["Database"] != "root.energy" {
		t.Fatalf("unexpected second database: %q", rows[1]["Database"])
	}
}

func TestParseRunningRegionCounts(t *testing.T) {
	output := `
+--------+------------+---------+------------+
|RegionId|        Type|   Status|    Database|
+--------+------------+---------+------------+
|   22235|SchemaRegion|  Running|root.emsplus|
|   22238|SchemaRegion|  Running| root.energy|
|   22239|SchemaRegion|Available| root.energy|
+--------+------------+---------+------------+
`

	counts := parseRunningRegionCounts(output)
	if counts["root.emsplus"] != 1 {
		t.Fatalf("expected emsplus running schema count 1, got %d", counts["root.emsplus"])
	}
	if counts["root.energy"] != 1 {
		t.Fatalf("expected energy running schema count 1, got %d", counts["root.energy"])
	}
}

func TestExtractSingleQueryValue(t *testing.T) {
	output := `
+-------------+-------------------------------------------+
|         Time|root.energy.__restore_probe.restore_check|
+-------------+-------------------------------------------+
|1770000000000|                              1770000000000|
+-------------+-------------------------------------------+
`

	value, err := extractSingleQueryValue(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value != "1770000000000" {
		t.Fatalf("unexpected value: %s", value)
	}
}

func TestIsRetryableImportError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "schema region missing",
			err:  errors.New("Execute FragmentInstance failed: The consensus group SchemaRegion[20896] doesn't exist"),
			want: true,
		},
		{
			name: "data region replica missing",
			err:  errors.New("301: Failed to get replicaSet of consensus group"),
			want: true,
		},
		{
			name: "readonly should not retry",
			err:  errors.New("Change system status to ReadOnly! Only query statements are permitted!"),
			want: false,
		},
		{
			name: "empty tsfile should not retry",
			err:  errors.New("TsFile /tmp/a.tsfile is empty"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableImportError(tt.err); got != tt.want {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestRestoreScanRootUsesNestedExtractPath(t *testing.T) {
	restorer := &IoTDBRestorer{
		config: &config.Config{
			IoTDB: config.IoTDBConfig{
				DataDir: "/iotdb/data",
			},
		},
	}

	if got := restorer.liveDataDir(); got != "/iotdb/data/datanode/data" {
		t.Fatalf("unexpected live data dir: %s", got)
	}

	if got := restorer.restoreScanRoot(); got != "/iotdb/data/iotdb/data/datanode/data" {
		t.Fatalf("unexpected restore scan root: %s", got)
	}
}
