package youtube

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDiagnosticListingSuppressesConfigAndCacheWrites(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "unexpected-write")
	t.Setenv("DIAGNOSTIC_WRITE_MARKER", marker)
	bin := filepath.Join(dir, "yt-dlp")
	// Emulate side effects enabled by a user's downloader configuration/cache.
	// Ordinary listing can use that setup; the diagnostic must suppress both.
	script := `#!/bin/sh
case " $* " in *" --ignore-config "*) ;; *) printf config > "$DIAGNOSTIC_WRITE_MARKER" ;; esac
case " $* " in *" --no-cache-dir "*) ;; *) printf cache > "$DIAGNOSTIC_WRITE_MARKER" ;; esac
printf '{"id":"abcdefghijk","title":"Example"}\n'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	old := Binary
	Binary = bin
	t.Cleanup(func() { Binary = old })
	list, err := InspectUploads(context.Background(), "https://youtube.com/@example", 1)
	if err != nil || len(list) != 1 || list[0].ID != "abcdefghijk" {
		t.Fatalf("inspection: %v %v", list, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("diagnostic permitted filesystem side effects")
	}
	if _, err := List(context.Background(), "https://youtube.com/@example", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("fixture did not exercise ordinary listing side effects")
	}
}
