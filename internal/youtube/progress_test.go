package youtube

import (
	"reflect"
	"testing"
)

func TestDownloadReporterReportsEachTenth(t *testing.T) {
	var got []string
	report := downloadReporter(func(s string) { got = append(got, s) })
	for _, line := range []string{
		progressMarker + " 1000 14800000 NA",
		progressMarker + " 1600000 14800000 NA",
		progressMarker + " 1700000 14800000 NA",
		progressMarker + " 7500000 NA 15000000", // total unknown, estimate used
		"[download] something else",
		progressMarker + " 14800000 14800000 NA",
	} {
		report(line)
	}
	want := []string{
		"downloading audio: 10% of 14.8 MB",
		"downloading audio: 50% of 15.0 MB",
		"downloading audio: 100% of 14.8 MB",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
