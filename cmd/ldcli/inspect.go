package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"text/tabwriter"

	ldparser "github.com/mail/go-ldparser"
)

// ---------------------------------------------------------------------------
// Response types — inspect
// ---------------------------------------------------------------------------

type inspectResponse struct {
	Files []fileInspect `json:"files"`
}

type fileInspect struct {
	Path          string              `json:"path"`
	Summary       inspectSummary      `json:"summary"`
	DataQuality   []qualityReport     `json:"data_quality"`
	Interesting   []interestingCh     `json:"interesting_channels"`
	ChannelGroups map[string][]string `json:"channel_groups"`
	Warnings      []string            `json:"warnings,omitempty"`
}

type inspectSummary struct {
	TotalChannels    int `json:"total_channels"`
	UsefulChannels   int `json:"useful_channels"`
	ConstantChannels int `json:"constant_channels"`
	InvalidChannels  int `json:"channels_with_invalid_samples"`
}

type qualityReport struct {
	Name           string  `json:"name"`
	Issue          string  `json:"issue"`                    // "none","constant","all_zero","has_invalid","low_variance"
	InvalidPct     float64 `json:"invalid_pct,omitempty"`
	Recommendation string  `json:"recommendation,omitempty"` // "skip" or "check"
}

type interestingCh struct {
	Name   string `json:"name"`
	Unit   string `json:"unit,omitempty"`
	Reason string `json:"reason"`
	Note   string `json:"note,omitempty"`
}

// ---------------------------------------------------------------------------
// inspect command
// ---------------------------------------------------------------------------

func runInspect(args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	lapFlag := fs.String("lap", "", "")
	format := fs.String("format", "json", "")
	filePaths, flagArgs := splitFilesAndFlags(args)
	_ = fs.Parse(flagArgs)
	files := loadFiles(filePaths)

	resp := inspectResponse{Files: make([]fileInspect, 0, len(files))}

	for _, fr := range files {
		from, to := 0.0, sessionEnd(fr)
		var warnings []string

		if *lapFlag != "" {
			wins, errMsg := buildWindows(*lapFlag, fr.Laps, to)
			if errMsg != "" || len(wins) == 0 {
				resp.Files = append(resp.Files, fileInspect{Path: fr.Path, Warnings: []string{errMsg}})
				continue
			}
			from, to = wins[0].from, wins[0].to
		}

		speedCh := findChannelFuzzy(fr.File, "Ground Speed", "Speed over Ground", "GPS Speed", "Vehicle Speed")

		quality := make([]qualityReport, 0, len(fr.File.Channels))
		var interesting []interestingCh
		constantCount, invalidCount := 0, 0

		for i := range fr.File.Channels {
			ch := &fr.File.Channels[i]
			data, _ := sliceChannel(ch, from, to)

			qr := analyzeQuality(ch.Name, data)
			quality = append(quality, qr)
			switch qr.Issue {
			case "constant", "all_zero", "low_variance", "all_invalid", "no_data":
				constantCount++
				continue
			case "has_invalid":
				invalidCount++
			}

			if len(data) == 0 {
				continue
			}

			// Always flag driver inputs
			n := strings.ToLower(ch.Name)
			if containsAny(n, "throttle", "steering", "steer angle", "current gear") ||
				(containsAny(n, "brake") && containsAny(n, "pres", "press")) {
				interesting = append(interesting, interestingCh{
					Name:   ch.Name,
					Unit:   ch.Unit,
					Reason: "driver_input",
					Note:   "fundamental driving data — always useful",
				})
				continue
			}

			// Primary speed channel
			if speedCh != nil && ch.Name == speedCh.Name {
				interesting = append(interesting, interestingCh{
					Name:   ch.Name,
					Unit:   ch.Unit,
					Reason: "primary_speed",
					Note:   "main vehicle speed channel",
				})
				continue
			}

			// High variance
			_, _, mean, std, _, _, _, ns := computeStats(data)
			if ns == 0 {
				continue
			}
			if mean != 0 && std/math.Abs(mean) > 0.3 {
				interesting = append(interesting, interestingCh{
					Name:   ch.Name,
					Unit:   ch.Unit,
					Reason: "high_variance",
					Note:   fmt.Sprintf("std/mean=%.2f — dynamic signal", std/math.Abs(mean)),
				})
				continue
			}

			// Strong drift across session
			if trend := sessionTrend(data); trend != "" {
				interesting = append(interesting, interestingCh{
					Name:   ch.Name,
					Unit:   ch.Unit,
					Reason: trend,
					Note:   fmt.Sprintf("mean shifts >10%% from start to end of session"),
				})
			}
		}

		useful := len(fr.File.Channels) - constantCount

		resp.Files = append(resp.Files, fileInspect{
			Path: fr.Path,
			Summary: inspectSummary{
				TotalChannels:    len(fr.File.Channels),
				UsefulChannels:   useful,
				ConstantChannels: constantCount,
				InvalidChannels:  invalidCount,
			},
			DataQuality:   quality,
			Interesting:   interesting,
			ChannelGroups: groupChannels(fr.File),
			Warnings:      warnings,
		})
	}

	switch *format {
	case "text":
		writeInspectText(&resp)
	default:
		writeJSON(resp)
	}
}

// ---------------------------------------------------------------------------
// Analysis helpers
// ---------------------------------------------------------------------------

func analyzeQuality(name string, data []float64) qualityReport {
	if len(data) == 0 {
		return qualityReport{Name: name, Issue: "no_data", Recommendation: "skip"}
	}
	invalidCount := 0
	for _, v := range data {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			invalidCount++
		}
	}
	if invalidCount == len(data) {
		return qualityReport{Name: name, Issue: "all_invalid", Recommendation: "skip", InvalidPct: 100}
	}
	if invalidCount > 0 {
		pct := round3(100 * float64(invalidCount) / float64(len(data)))
		return qualityReport{Name: name, Issue: "has_invalid", InvalidPct: pct, Recommendation: "check"}
	}
	mn, mx, mean, std, _, _, _, _ := computeStats(data)
	if mx == mn {
		if mx == 0 {
			return qualityReport{Name: name, Issue: "all_zero", Recommendation: "skip"}
		}
		return qualityReport{Name: name, Issue: "constant", Recommendation: "skip"}
	}
	if mean != 0 && std/math.Abs(mean) < 0.01 {
		return qualityReport{Name: name, Issue: "low_variance", Recommendation: "skip"}
	}
	_ = mn
	return qualityReport{Name: name, Issue: "none"}
}

// sessionTrend returns a trend reason string if the channel drifts > 10%
// from its first-quarter mean to its last-quarter mean.
func sessionTrend(data []float64) string {
	if len(data) < 8 {
		return ""
	}
	q := len(data) / 4
	_, _, meanFirst, _, _, _, _, _ := computeStats(data[:q])
	_, _, meanLast, _, _, _, _, _ := computeStats(data[len(data)-q:])
	if meanFirst == 0 {
		return ""
	}
	ratio := (meanLast - meanFirst) / math.Abs(meanFirst)
	if ratio > 0.10 {
		return "strong_trend_increase"
	}
	if ratio < -0.10 {
		return "strong_trend_decrease"
	}
	return ""
}

// groupChannels categorises all channels in a file into logical groups.
func groupChannels(f *ldparser.File) map[string][]string {
	groups := map[string][]string{
		"driver_inputs": {},
		"speeds":        {},
		"tyres":         {},
		"suspension":    {},
		"temperatures":  {},
		"pressures":     {},
		"g_forces":      {},
		"engine":        {},
		"other":         {},
	}
	for _, ch := range f.Channels {
		n := strings.ToLower(ch.Name)
		switch {
		case containsAny(n, "throttle") ||
			(containsAny(n, "brake") && containsAny(n, "pres", "press")) ||
			containsAny(n, "steering", "steer angle", "gear"):
			groups["driver_inputs"] = append(groups["driver_inputs"], ch.Name)
		case containsAny(n, "speed", "velocity"):
			groups["speeds"] = append(groups["speeds"], ch.Name)
		case containsAny(n, "tyre", "tire"):
			groups["tyres"] = append(groups["tyres"], ch.Name)
		case containsAny(n, "susp", "ride height", "spring", "damper", "bump", "rebound", "packer"):
			groups["suspension"] = append(groups["suspension"], ch.Name)
		case containsAny(n, "temp", "temperature"):
			groups["temperatures"] = append(groups["temperatures"], ch.Name)
		case containsAny(n, "pres", "press") && !containsAny(n, "brake"):
			groups["pressures"] = append(groups["pressures"], ch.Name)
		case containsAny(n, "g force", "g lat", "g long", "accel", "gyro"):
			groups["g_forces"] = append(groups["g_forces"], ch.Name)
		case containsAny(n, "rpm", "engine", "fuel", "manifold", "water", "oil"):
			groups["engine"] = append(groups["engine"], ch.Name)
		default:
			groups["other"] = append(groups["other"], ch.Name)
		}
	}
	// Drop empty groups to keep output compact
	for k, v := range groups {
		if len(v) == 0 {
			delete(groups, k)
		}
	}
	return groups
}

// ---------------------------------------------------------------------------
// Output formatters — inspect
// ---------------------------------------------------------------------------

func writeInspectText(resp *inspectResponse) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, fi := range resp.Files {
		fmt.Fprintf(tw, "=== %s ===\n", fi.Path)
		s := fi.Summary
		fmt.Fprintf(tw, "Channels:\t%d total, %d useful, %d constant/flat, %d with invalid samples\n\n",
			s.TotalChannels, s.UsefulChannels, s.ConstantChannels, s.InvalidChannels)

		if len(fi.Interesting) > 0 {
			fmt.Fprintln(tw, "INTERESTING CHANNELS")
			fmt.Fprintln(tw, "NAME\tUNIT\tREASON\tNOTE")
			for _, ch := range fi.Interesting {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ch.Name, ch.Unit, ch.Reason, ch.Note)
			}
			fmt.Fprintln(tw)
		}

		fmt.Fprintln(tw, "CHANNEL GROUPS")
		for grp, chs := range fi.ChannelGroups {
			if len(chs) > 0 {
				fmt.Fprintf(tw, "  %-16s\t%s\n", grp+":", strings.Join(chs, ", "))
			}
		}
		fmt.Fprintln(tw)

		problems := 0
		for _, q := range fi.DataQuality {
			if q.Issue != "none" {
				problems++
			}
		}
		if problems > 0 {
			fmt.Fprintf(tw, "DATA QUALITY ISSUES (%d channels)\n", problems)
			fmt.Fprintln(tw, "NAME\tISSUE\tINVALID%\tRECOMMENDATION")
			for _, q := range fi.DataQuality {
				if q.Issue == "none" {
					continue
				}
				fmt.Fprintf(tw, "%s\t%s\t%.1f\t%s\n", q.Name, q.Issue, q.InvalidPct, q.Recommendation)
			}
			fmt.Fprintln(tw)
		}
	}
	tw.Flush()
}

// ---------------------------------------------------------------------------
// Misc helpers
// ---------------------------------------------------------------------------

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
