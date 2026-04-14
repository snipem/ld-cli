package ldparser

import (
	"testing"
)

// Test files used for benchmarks. The LMU file is the largest available
// in testdata; add a bigger file here if you have one (e.g. a full 45-min race).
var benchFiles = []struct {
	name string
	path string
}{
	{"gt7-alsace-mini (1.7 MB, 37ch, 3 laps)", "testdata/gt7-alsace-mini.ld"},
	{"lmu-bmwgt3-spa (3.5 MB, 78ch, 2 laps)", "testdata/lmu-bmwgt3-spa-q1.ld"},
	{"ac-tatuusfa1-spa (3.0 MB, 166ch, 2 laps)", "testdata/ac-tatuusfa1-spa.ld"},
}

// BenchmarkParseFile measures the cost of a full parse (all channel data decoded).
func BenchmarkParseFile(b *testing.B) {
	for _, tc := range benchFiles {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				f, err := ParseFile(tc.path)
				if err != nil {
					b.Fatal(err)
				}
				_ = f
			}
		})
	}
}

// BenchmarkParseMetaFile measures the cost of a metadata-only parse
// (header + channel catalogue + lap channels only).
func BenchmarkParseMetaFile(b *testing.B) {
	for _, tc := range benchFiles {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				f, err := ParseMetaFile(tc.path)
				if err != nil {
					b.Fatal(err)
				}
				_ = f
			}
		})
	}
}

// BenchmarkParseMetaAndDetectLaps is the realistic "scan many files" scenario:
// open file, get metadata, get lap list, close. This is what a file-browser or
// network-facing API would do before the user picks a lap to load fully.
func BenchmarkParseMetaAndDetectLaps(b *testing.B) {
	for _, tc := range benchFiles {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				f, err := ParseMetaFile(tc.path)
				if err != nil {
					b.Fatal(err)
				}
				laps := f.DetectLaps()
				if len(laps) == 0 {
					b.Fatalf("no laps detected in %s", tc.path)
				}
			}
		})
	}
}

// BenchmarkParseFileAndDetectLaps is the baseline for comparison: full parse
// then lap detection (current behavior of ldcli info).
func BenchmarkParseFileAndDetectLaps(b *testing.B) {
	for _, tc := range benchFiles {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				f, err := ParseFile(tc.path)
				if err != nil {
					b.Fatal(err)
				}
				laps := f.DetectLaps()
				if len(laps) == 0 {
					b.Fatalf("no laps detected in %s", tc.path)
				}
			}
		})
	}
}
