package parser

import (
	"strings"
	"testing"
	"time"
)

func TestParseKlogLine(t *testing.T) {
	p, err := New(5, 1, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	line := `2024-12-30T10:46:29.390512670Z I1230 10:46:29.390512       1 node_controller.go:1056] No nodes available for updates`
	p.parseLine(line)

	got := p.Summary()
	infos := got["info"]
	if len(infos) != 1 {
		t.Fatalf("expected 1 info entry, got %d", len(infos))
	}
	if infos[0].Source != "node_controller.go:1056" {
		t.Errorf("source = %q, want node_controller.go:1056", infos[0].Source)
	}
	if infos[0].Occurrences != 1 {
		t.Errorf("occurrences = %d, want 1", infos[0].Occurrences)
	}
	msg, ok := infos[0].Recent[0].Log.(string)
	if !ok || msg != "No nodes available for updates" {
		t.Errorf("recent log = %v, want klog message string", infos[0].Recent[0].Log)
	}
}

func TestParseJSONLine(t *testing.T) {
	p, _ := New(5, 1, 0)
	line := `2024-12-30T10:46:29.390512670Z {"level":"error","caller":"foo.go:42","msg":"boom","error":"bad thing"}`
	p.parseLine(line)

	errs := p.Summary()["error"]
	if len(errs) != 1 {
		t.Fatalf("expected 1 error entry, got %d", len(errs))
	}
	if errs[0].Source != "foo.go:42" {
		t.Errorf("source = %q, want foo.go:42", errs[0].Source)
	}
	full, ok := errs[0].Recent[0].Log.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON payload to be map, got %T", errs[0].Recent[0].Log)
	}
	if full["error"] != "bad thing" {
		t.Errorf("full payload missing error field: %v", full)
	}
}

func TestSeverityAliases(t *testing.T) {
	cases := []struct {
		level string
		want  string
	}{
		{"info", "info"},
		{"warn", "warning"},
		{"warning", "warning"},
		{"error", "error"},
		{"fatal", "fatal"},
		{"INFO", "info"}, // case-insensitive
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			p, _ := New(5, 1, 0)
			line := `2024-12-30T10:46:29.390512670Z {"level":"` + tc.level + `","caller":"x.go:1","msg":"m"}`
			p.parseLine(line)
			if got := len(p.Summary()[tc.want]); got != 1 {
				t.Errorf("severity %q: got %d entries in bucket %q, want 1", tc.level, got, tc.want)
			}
		})
	}
}

func TestRingOverflow(t *testing.T) {
	p, _ := New(3, 1, 0)
	for i := 0; i < 10; i++ {
		line := `2024-12-30T10:46:29.390512670Z I1230 10:46:29.390512       1 a.go:1] msg`
		p.parseLine(line)
	}
	entry := p.Summary()["info"][0]
	if entry.Occurrences != 10 {
		t.Errorf("occurrences = %d, want 10", entry.Occurrences)
	}
	if len(entry.Recent) != 3 {
		t.Errorf("recent len = %d, want 3 (capped)", len(entry.Recent))
	}
}

func TestMalformedJSONCapturedAsUnstructured(t *testing.T) {
	p, _ := New(5, 1, 0)
	// Bad JSON should not crash and should be captured as unstructured.
	bad := `2024-12-30T10:46:29.390512670Z {not valid json}`
	good := `2024-12-30T10:46:29.390512670Z {"level":"info","caller":"x.go:1","msg":"ok"}`
	r := strings.NewReader(bad + "\n" + good + "\n")
	if err := p.Consume(r); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	summary := p.Summary()

	// Good line should be in info
	if got := len(summary["info"]); got != 1 {
		t.Errorf("info entries = %d, want 1 (good line only)", got)
	}

	// Bad line should be in unstructured
	if got := len(summary["unstructured"]); got != 1 {
		t.Errorf("unstructured entries = %d, want 1 (bad JSON)", got)
	}

	// Verify the malformed JSON is preserved without timestamp prefix
	msg, ok := summary["unstructured"][0].Recent[0].Log.(string)
	want := "{not valid json}"
	if !ok || msg != want {
		t.Errorf("unstructured log = %q, want %q", msg, want)
	}
}

func TestLineWithoutTimestampCapturedAsUnstructured(t *testing.T) {
	p, _ := New(5, 1, 0)
	// Lines without a timestamp should be captured as unstructured, not error.
	r := strings.NewReader("this is not a structured log line\n")
	if err := p.Consume(r); err != nil {
		t.Fatalf("Consume: unexpected error: %v", err)
	}
	summary := p.Summary()
	if got := len(summary["unstructured"]); got != 1 {
		t.Errorf("unstructured entries = %d, want 1", got)
	}
}

func TestNewClampsLastN(t *testing.T) {
	for _, n := range []int{0, -1, 11, 999} {
		p, err := New(n, 1, 0)
		if err != nil {
			t.Fatalf("New(%d, 1): %v", n, err)
		}
		if p.lastN != 5 {
			t.Errorf("New(%d, 1) lastN = %d, want 5 fallback", n, p.lastN)
		}
	}
}

func TestRecentKeyMatchesLastN(t *testing.T) {
	cases := []int{1, 3, 5, 10}
	for _, n := range cases {
		p, _ := New(n, 1, 0)
		if p.recentKey != "last_occurrences" {
			t.Errorf("New(%d, 1) recentKey = %q, want %q", n, p.recentKey, "last_occurrences")
		}
	}
}

func TestNewClampsFirstN(t *testing.T) {
	for _, n := range []int{0, -1, 11, 999} {
		p, err := New(5, n, 0)
		if err != nil {
			t.Fatalf("New(5, %d): %v", n, err)
		}
		if p.firstN != 1 {
			t.Errorf("New(5, %d) firstN = %d, want 1 fallback", n, p.firstN)
		}
	}
}

func TestFirstOccurrences(t *testing.T) {
	p, _ := New(3, 2, 0)
	// Add 5 occurrences of the same log line
	for i := 0; i < 5; i++ {
		line := `2024-12-30T10:46:29.390512670Z I1230 10:46:29.390512       1 test.go:10] msg`
		p.parseLine(line)
	}

	entry := p.Summary()["info"][0]
	if entry.Occurrences != 5 {
		t.Errorf("occurrences = %d, want 5", entry.Occurrences)
	}
	// Should only keep first 2
	if len(entry.First) != 2 {
		t.Errorf("first occurrences len = %d, want 2", len(entry.First))
	}
	// Should keep last 3
	if len(entry.Recent) != 3 {
		t.Errorf("recent occurrences len = %d, want 3", len(entry.Recent))
	}
}

func TestFrequencyCalculation(t *testing.T) {
	tests := []struct {
		name        string
		occurrences int
		firstTS     string
		lastTS      string
		want        string
	}{
		{
			name:        "per second - 10 logs in 1 second",
			occurrences: 10,
			firstTS:     "2024-12-30T10:46:29.000000000Z",
			lastTS:      "2024-12-30T10:46:30.000000000Z",
			want:        "10/s",
		},
		{
			name:        "per minute - 120 logs in 60 seconds (exactly 1 minute)",
			occurrences: 120,
			firstTS:     "2024-12-30T10:46:00.000000000Z",
			lastTS:      "2024-12-30T10:47:00.000000000Z",
			want:        "120/m",
		},
		{
			name:        "per minute - 5 logs in 10 minutes",
			occurrences: 5,
			firstTS:     "2024-12-30T10:00:00.000000000Z",
			lastTS:      "2024-12-30T10:10:00.000000000Z",
			want:        "0.5/m",
		},
		{
			name:        "per minute - 10 logs in 5 minutes",
			occurrences: 10,
			firstTS:     "2024-12-30T10:00:00.000000000Z",
			lastTS:      "2024-12-30T10:05:00.000000000Z",
			want:        "2.0/m",
		},
		{
			name:        "per minute - 30 logs in 15 minutes",
			occurrences: 30,
			firstTS:     "2024-12-30T10:00:00.000000000Z",
			lastTS:      "2024-12-30T10:15:00.000000000Z",
			want:        "2.0/m",
		},
		{
			name:        "per minute - 5 logs in 7 minutes (actual case from user)",
			occurrences: 5,
			firstTS:     "2026-05-04T07:50:45.581233383Z",
			lastTS:      "2026-05-04T07:57:45.637694164Z",
			want:        "0.7/m",
		},
		{
			name:        "per hour - 4 logs in 2 hours",
			occurrences: 4,
			firstTS:     "2024-12-30T10:00:00.000000000Z",
			lastTS:      "2024-12-30T12:00:00.000000000Z",
			want:        "2.0/h",
		},
		{
			name:        "per hour - 10 logs in 5 hours",
			occurrences: 10,
			firstTS:     "2024-12-30T10:00:00.000000000Z",
			lastTS:      "2024-12-30T15:00:00.000000000Z",
			want:        "2.0/h",
		},
		{
			name:        "per hour - 2 logs in 15 hours",
			occurrences: 2,
			firstTS:     "2026-04-29T16:51:29.280896980Z",
			lastTS:      "2026-04-30T07:53:21.563792653Z",
			want:        "0.1/h",
		},
		{
			name:        "per day - 2 logs in 4 days",
			occurrences: 2,
			firstTS:     "2024-12-26T10:00:00.000000000Z",
			lastTS:      "2024-12-30T10:00:00.000000000Z",
			want:        "0.5/d",
		},
		{
			name:        "per day - 5 logs in 10 days",
			occurrences: 5,
			firstTS:     "2024-12-20T10:00:00.000000000Z",
			lastTS:      "2024-12-30T10:00:00.000000000Z",
			want:        "0.5/d",
		},
		{
			name:        "single occurrence",
			occurrences: 1,
			firstTS:     "2024-12-30T10:00:00.000000000Z",
			lastTS:      "2024-12-30T10:00:00.000000000Z",
			want:        "",
		},
		{
			name:        "same timestamp - duration too short",
			occurrences: 5,
			firstTS:     "2024-12-30T10:00:00.000000000Z",
			lastTS:      "2024-12-30T10:00:00.000000000Z",
			want:        "",
		},
		{
			name:        "burst in milliseconds - duration too short",
			occurrences: 57,
			firstTS:     "2026-04-29T16:51:23.030739787Z",
			lastTS:      "2026-04-29T16:51:23.035073027Z",
			want:        "",
		},
		{
			name:        "invalid first timestamp",
			occurrences: 10,
			firstTS:     "invalid",
			lastTS:      "2024-12-30T10:00:00.000000000Z",
			want:        "",
		},
		{
			name:        "invalid last timestamp",
			occurrences: 10,
			firstTS:     "2024-12-30T10:00:00.000000000Z",
			lastTS:      "invalid",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateFrequency(tt.occurrences, tt.firstTS, tt.lastTS)
			if got != tt.want {
				t.Errorf("calculateFrequency(%d, %q, %q) = %q, want %q",
					tt.occurrences, tt.firstTS, tt.lastTS, got, tt.want)
			}
		})
	}
}

func TestFrequencyInSummary(t *testing.T) {
	p, _ := New(5, 1, 0)

	// Add logs with different timestamps
	lines := []string{
		`2024-12-30T10:00:00.000000000Z I1230 10:00:00.000000       1 freq.go:1] msg1`,
		`2024-12-30T10:00:01.000000000Z I1230 10:00:01.000000       1 freq.go:1] msg2`,
		`2024-12-30T10:00:02.000000000Z I1230 10:00:02.000000       1 freq.go:1] msg3`,
		`2024-12-30T10:00:03.000000000Z I1230 10:00:03.000000       1 freq.go:1] msg4`,
		`2024-12-30T10:00:04.000000000Z I1230 10:00:04.000000       1 freq.go:1] msg5`,
	}

	for _, line := range lines {
		p.parseLine(line)
	}

	summary := p.Summary()
	entry := summary["info"][0]

	if entry.Occurrences != 5 {
		t.Errorf("occurrences = %d, want 5", entry.Occurrences)
	}

	// 5 occurrences over 4 seconds = 1.25/s, shown as 1.2/s (one decimal)
	if entry.Frequency != "1.2/s" {
		t.Errorf("frequency = %q, want %q", entry.Frequency, "1.2/s")
	}
}

func TestSinceFiltering(t *testing.T) {
	// Create parser with 1 hour since filter
	p, _ := New(5, 1, 1*time.Hour)

	lines := []string{
		// Old logs (> 1 hour before latest)
		`2024-12-30T10:00:00.000000000Z I1230 10:00:00.000000       1 test.go:1] old1`,
		`2024-12-30T10:30:00.000000000Z I1230 10:30:00.000000       1 test.go:1] old2`,
		// Recent logs (within 1 hour of latest)
		`2024-12-30T11:30:00.000000000Z I1230 11:30:00.000000       1 test.go:1] recent1`,
		`2024-12-30T12:00:00.000000000Z I1230 12:00:00.000000       1 test.go:1] recent2`,
		`2024-12-30T12:15:00.000000000Z I1230 12:15:00.000000       1 test.go:1] recent3`,
	}

	for _, line := range lines {
		p.parseLine(line)
	}

	summary := p.Summary()
	entries := summary["info"]

	if len(entries) != 1 {
		t.Fatalf("expected 1 source, got %d", len(entries))
	}

	entry := entries[0]
	// Should only have 3 recent logs (within 1 hour of 12:15:00)
	if entry.Occurrences != 3 {
		t.Errorf("occurrences = %d, want 3", entry.Occurrences)
	}

	// Check that we only have recent logs
	allLogs := append(entry.First, entry.Recent...)
	for _, occ := range allLogs {
		if strings.Contains(occ.Log.(string), "old") {
			t.Errorf("found old log that should be filtered: %v", occ.Log)
		}
	}
}

func TestSinceFilteringMultipleSources(t *testing.T) {
	p, _ := New(5, 1, 30*time.Minute)

	lines := []string{
		// source1: has both old and recent logs
		`2024-12-30T10:00:00.000000000Z I1230 10:00:00.000000       1 source1.go:1] msg`,
		`2024-12-30T10:45:00.000000000Z I1230 10:45:00.000000       1 source1.go:1] msg`,
		// source2: only old logs (should be filtered out)
		`2024-12-30T10:00:00.000000000Z W1230 10:00:00.000000       1 source2.go:1] msg`,
		`2024-12-30T10:10:00.000000000Z W1230 10:10:00.000000       1 source2.go:1] msg`,
		// Latest log (defines cutoff point)
		`2024-12-30T11:00:00.000000000Z E1230 11:00:00.000000       1 source3.go:1] msg`,
	}

	for _, line := range lines {
		p.parseLine(line)
	}

	summary := p.Summary()

	// source1 should have 1 occurrence (only the 10:45 log is within 30 min of 11:00)
	if len(summary["info"]) != 1 {
		t.Errorf("info sources = %d, want 1", len(summary["info"]))
	}
	if summary["info"][0].Occurrences != 1 {
		t.Errorf("source1 occurrences = %d, want 1", summary["info"][0].Occurrences)
	}

	// source2 should be completely filtered out (all logs > 30 min old)
	if len(summary["warning"]) != 0 {
		t.Errorf("warning sources = %d, want 0 (should be filtered)", len(summary["warning"]))
	}

	// source3 should have 1 occurrence
	if len(summary["error"]) != 1 {
		t.Errorf("error sources = %d, want 1", len(summary["error"]))
	}
	if summary["error"][0].Occurrences != 1 {
		t.Errorf("source3 occurrences = %d, want 1", summary["error"][0].Occurrences)
	}
}

func TestSinceNoFilter(t *testing.T) {
	// Parser with no since filter (0 duration)
	p, _ := New(5, 1, 0)

	lines := []string{
		`2024-12-30T10:00:00.000000000Z I1230 10:00:00.000000       1 test.go:1] msg1`,
		`2024-12-30T12:00:00.000000000Z I1230 12:00:00.000000       1 test.go:1] msg2`,
	}

	for _, line := range lines {
		p.parseLine(line)
	}

	summary := p.Summary()
	entry := summary["info"][0]

	// Should have both occurrences (no filtering)
	if entry.Occurrences != 2 {
		t.Errorf("occurrences = %d, want 2 (no filter)", entry.Occurrences)
	}
}

func TestSinceWithOutOfOrderTimestamps(t *testing.T) {
	// Parser with 2 hour since filter
	p, _ := New(5, 1, 2*time.Hour)

	// Timestamps are out of order: latest (10:10) appears before 10:05
	lines := []string{
		`2024-12-30T09:00:00.000000000Z I1230 09:00:00.000000       1 test.go:1] oldest`,
		`2024-12-30T10:00:00.000000000Z I1230 10:00:00.000000       1 test.go:1] log 2`,
		`2024-12-30T10:10:00.000000000Z I1230 10:10:00.000000       1 test.go:1] latest`,
		`2024-12-30T10:05:00.000000000Z I1230 10:05:00.000000       1 test.go:1] log 4 (out of order)`,
	}

	for _, line := range lines {
		p.parseLine(line)
	}

	summary := p.Summary()
	entry := summary["info"][0]

	// All 4 logs are within 2 hours of latest (10:10)
	if entry.Occurrences != 4 {
		t.Errorf("occurrences = %d, want 4", entry.Occurrences)
	}

	// Frequency should be based on actual time range: 09:00 to 10:10 = 70 minutes
	// 4 logs / 70 minutes ≈ 3.4/h (duration is > 1 hour, so uses /h unit)
	if entry.Frequency != "3.4/h" {
		t.Errorf("frequency = %q, want \"3.4/h\" (based on chronological min/max, not array order)", entry.Frequency)
	}
}

func TestShortLineCapturedAsUnstructured(t *testing.T) {
	p, _ := New(5, 1, 0)
	// Lines shorter than 31 chars (no valid timestamp) should be captured as
	// unstructured, not rejected.
	r := strings.NewReader("short\n")
	if err := p.Consume(r); err != nil {
		t.Fatalf("Consume: unexpected error: %v", err)
	}
	if got := len(p.Summary()["unstructured"]); got != 1 {
		t.Errorf("unstructured entries = %d, want 1", got)
	}
}

func TestUnstructuredMultipleLinesWithTimestamp(t *testing.T) {
	p, _ := New(5, 1, 0)
	// Unstructured lines with valid timestamps
	input := `2024-12-30T10:46:29.390512670Z Random line 1
2024-12-30T10:46:30.390512670Z Random line 2
2024-12-30T10:46:31.390512670Z Random line 3
`
	r := strings.NewReader(input)
	if err := p.Consume(r); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	unstructured := p.Summary()["unstructured"]
	if len(unstructured) != 1 {
		t.Fatalf("expected 1 unstructured source, got %d", len(unstructured))
	}

	// Should aggregate all 3 lines under same source
	if unstructured[0].Occurrences != 3 {
		t.Errorf("occurrences = %d, want 3", unstructured[0].Occurrences)
	}

	// Check recent contains all 3 lines
	if len(unstructured[0].Recent) != 3 {
		t.Errorf("recent len = %d, want 3", len(unstructured[0].Recent))
	}
}

func TestUnstructuredWithStructured(t *testing.T) {
	p, _ := New(5, 1, 0)
	input := `2024-12-30T10:46:28.390512670Z Random line 1
2024-12-30T10:46:29.390512670Z I1230 10:46:29.390512  1 test.go:1] Info message
2024-12-30T10:46:30.390512670Z Random line 2
2024-12-30T10:46:31.390512670Z {"level":"error","caller":"foo.go:42","msg":"Error message"}
2024-12-30T10:46:32.390512670Z Random line 3
`
	r := strings.NewReader(input)
	if err := p.Consume(r); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	summary := p.Summary()

	// Should have info entry
	if len(summary["info"]) != 1 {
		t.Errorf("info entries = %d, want 1", len(summary["info"]))
	}

	// Should have error entry
	if len(summary["error"]) != 1 {
		t.Errorf("error entries = %d, want 1", len(summary["error"]))
	}

	// Should have unstructured entries
	if len(summary["unstructured"]) != 1 {
		t.Errorf("unstructured entries = %d, want 1", len(summary["unstructured"]))
	}
	if summary["unstructured"][0].Occurrences != 3 {
		t.Errorf("unstructured occurrences = %d, want 3", summary["unstructured"][0].Occurrences)
	}
}

func TestUnstructuredJSONWithUnknownSeverity(t *testing.T) {
	p, _ := New(5, 1, 0)
	// Valid JSON structure but unrecognized severity level
	line := `2024-12-30T10:46:29.390512670Z {"level":"debug","caller":"test.go:1","msg":"debug msg"}`
	r := strings.NewReader(line + "\n")
	if err := p.Consume(r); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	summary := p.Summary()

	// Should be in unstructured, not skipped
	if len(summary["unstructured"]) != 1 {
		t.Fatalf("expected 1 unstructured entry, got %d", len(summary["unstructured"]))
	}

	// Content without timestamp should be preserved
	msg, ok := summary["unstructured"][0].Recent[0].Log.(string)
	want := `{"level":"debug","caller":"test.go:1","msg":"debug msg"}`
	if !ok || msg != want {
		t.Errorf("log = %q, want %q", msg, want)
	}
}

func TestUnstructuredEmptyOutput(t *testing.T) {
	p, _ := New(5, 1, 0)
	// Only valid structured logs
	input := `2024-12-30T10:46:29.390512670Z I1230 10:46:29.390512  1 test.go:1] Info
2024-12-30T10:46:29.390512670Z {"level":"error","caller":"foo.go:42","msg":"Error"}
`
	r := strings.NewReader(input)
	if err := p.Consume(r); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	summary := p.Summary()

	// Unstructured should exist but be empty
	unstructured, exists := summary["unstructured"]
	if !exists {
		t.Errorf("unstructured key missing from summary")
	}
	if len(unstructured) != 0 {
		t.Errorf("unstructured entries = %d, want 0 (empty slice)", len(unstructured))
	}
}

func TestUnstructuredKlogUnknownSeverity(t *testing.T) {
	p, _ := New(5, 1, 0)
	// klog pattern matches but severity letter is unknown (X instead of I/W/E/F)
	line := `2024-12-30T10:46:29.390512670Z X1230 10:46:29.390512  1 test.go:1] Unknown severity`
	r := strings.NewReader(line + "\n")
	if err := p.Consume(r); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	summary := p.Summary()

	// Should be captured as unstructured
	if len(summary["unstructured"]) != 1 {
		t.Fatalf("expected 1 unstructured entry, got %d", len(summary["unstructured"]))
	}

	// Content without timestamp should be preserved
	msg, ok := summary["unstructured"][0].Recent[0].Log.(string)
	want := `X1230 10:46:29.390512  1 test.go:1] Unknown severity`
	if !ok || msg != want {
		t.Errorf("log = %q, want %q", msg, want)
	}
}

func TestTimelineMinuteBinning(t *testing.T) {
	p, _ := New(5, 1, 0)
	p.SetTimelineInterval("minute")

	lines := []string{
		`2024-12-30T10:46:29.390512670Z I1230 10:46:29.390512  1 node_controller.go:1056] No nodes available`,
		`2024-12-30T10:46:45.390512670Z I1230 10:46:45.390512  1 node_controller.go:1056] Still no nodes`,
		`2024-12-30T10:47:01.390512670Z {"level":"error","caller":"foo.go:42","msg":"boom"}`,
		`2024-12-30T11:01:00.390512670Z I1230 11:01:00.390512  1 node_controller.go:1056] Back online`,
	}
	for _, l := range lines {
		p.parseLine(l)
	}

	tl := p.Timeline()

	if len(tl) != 3 {
		t.Fatalf("expected 3 bins, got %d", len(tl))
	}

	// Bins must be sorted chronologically
	if tl[0].Interval != "2024-12-30T10:46" {
		t.Errorf("tl[0].Interval = %q, want 2024-12-30T10:46", tl[0].Interval)
	}
	if tl[1].Interval != "2024-12-30T10:47" {
		t.Errorf("tl[1].Interval = %q, want 2024-12-30T10:47", tl[1].Interval)
	}
	if tl[2].Interval != "2024-12-30T11:01" {
		t.Errorf("tl[2].Interval = %q, want 2024-12-30T11:01", tl[2].Interval)
	}

	// First bin: 2 messages from node_controller.go:1056
	if len(tl[0].Sources) != 1 {
		t.Fatalf("bin[0] sources = %d, want 1", len(tl[0].Sources))
	}
	if tl[0].Sources[0].Source != "node_controller.go:1056" {
		t.Errorf("bin[0] source = %q, want node_controller.go:1056", tl[0].Sources[0].Source)
	}
	if tl[0].Sources[0].Count != 2 {
		t.Errorf("bin[0] source count = %d, want 2", tl[0].Sources[0].Count)
	}
	if tl[0].Sources[0].Sample == nil {
		t.Errorf("bin[0] sample is nil, want a log message")
	}
	if tl[0].Count != 2 {
		t.Errorf("bin[0] total count = %d, want 2", tl[0].Count)
	}

	// Second bin: 1 error from foo.go:42
	if len(tl[1].Sources) != 1 {
		t.Fatalf("bin[1] sources = %d, want 1", len(tl[1].Sources))
	}
	if tl[1].Sources[0].Source != "foo.go:42" {
		t.Errorf("bin[1] source = %q, want foo.go:42", tl[1].Sources[0].Source)
	}
}

func TestTimelineHourBinning(t *testing.T) {
	p, _ := New(5, 1, 0)
	p.SetTimelineInterval("hour")

	lines := []string{
		`2024-12-30T10:46:29.390512670Z I1230 10:46:29.390512  1 a.go:1] msg1`,
		`2024-12-30T10:59:00.390512670Z I1230 10:59:00.390512  1 a.go:1] msg2`,
		`2024-12-30T11:01:00.390512670Z I1230 11:01:00.390512  1 a.go:1] msg3`,
	}
	for _, l := range lines {
		p.parseLine(l)
	}

	tl := p.Timeline()

	if len(tl) != 2 {
		t.Fatalf("expected 2 hour bins, got %d", len(tl))
	}
	if tl[0].Interval != "2024-12-30T10" {
		t.Errorf("bin[0] = %q, want 2024-12-30T10", tl[0].Interval)
	}
	if tl[0].Sources[0].Count != 2 {
		t.Errorf("bin[0] count = %d, want 2", tl[0].Sources[0].Count)
	}
	if tl[1].Interval != "2024-12-30T11" {
		t.Errorf("bin[1] = %q, want 2024-12-30T11", tl[1].Interval)
	}
}

func TestTimelineSecondBinning(t *testing.T) {
	p, _ := New(5, 1, 0)
	p.SetTimelineInterval("second")

	lines := []string{
		`2024-12-30T10:46:29.000000000Z I1230 10:46:29.000000  1 a.go:1] msg1`,
		`2024-12-30T10:46:29.999999999Z I1230 10:46:29.999999  1 a.go:1] msg2`,
		`2024-12-30T10:46:30.000000000Z I1230 10:46:30.000000  1 a.go:1] msg3`,
	}
	for _, l := range lines {
		p.parseLine(l)
	}

	tl := p.Timeline()

	if len(tl) != 2 {
		t.Fatalf("expected 2 second bins, got %d", len(tl))
	}
	if tl[0].Interval != "2024-12-30T10:46:29" {
		t.Errorf("bin[0] = %q, want 2024-12-30T10:46:29", tl[0].Interval)
	}
	if tl[0].Sources[0].Count != 2 {
		t.Errorf("bin[0] count = %d, want 2", tl[0].Sources[0].Count)
	}
}

func TestTimelineMultipleSources(t *testing.T) {
	p, _ := New(5, 1, 0)
	p.SetTimelineInterval("minute")

	lines := []string{
		`2024-12-30T10:46:01.000000000Z I1230 10:46:01.000000  1 a.go:1] msg`,
		`2024-12-30T10:46:02.000000000Z I1230 10:46:02.000000  1 b.go:1] msg`,
		`2024-12-30T10:46:03.000000000Z I1230 10:46:03.000000  1 a.go:1] msg`,
		`2024-12-30T10:46:04.000000000Z I1230 10:46:04.000000  1 a.go:1] msg`,
	}
	for _, l := range lines {
		p.parseLine(l)
	}

	tl := p.Timeline()

	if len(tl) != 1 {
		t.Fatalf("expected 1 bin, got %d", len(tl))
	}

	sources := tl[0].Sources
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(sources))
	}

	// Sources sorted by count ascending: b.go:1 (1) then a.go:1 (3)
	if sources[0].Source != "b.go:1" || sources[0].Count != 1 {
		t.Errorf("sources[0] = {%q, %d}, want {b.go:1, 1}", sources[0].Source, sources[0].Count)
	}
	if sources[1].Source != "a.go:1" || sources[1].Count != 3 {
		t.Errorf("sources[1] = {%q, %d}, want {a.go:1, 3}", sources[1].Source, sources[1].Count)
	}
}

func TestTimelineDefaultBinFallback(t *testing.T) {
	p, _ := New(5, 1, 0)
	p.SetTimelineInterval("invalid") // should fall back to minute

	lines := []string{
		`2024-12-30T10:46:01.000000000Z I1230 10:46:01.000000  1 a.go:1] msg1`,
		`2024-12-30T10:46:59.000000000Z I1230 10:46:59.000000  1 a.go:1] msg2`,
		`2024-12-30T10:47:00.000000000Z I1230 10:47:00.000000  1 a.go:1] msg3`,
	}
	for _, l := range lines {
		p.parseLine(l)
	}

	tl := p.Timeline()

	if len(tl) != 2 {
		t.Fatalf("expected 2 minute bins (fallback), got %d", len(tl))
	}
}

func TestTimelineBinCount(t *testing.T) {
	t.Run("single source", func(t *testing.T) {
		p, _ := New(5, 1, 0)
		p.SetTimelineInterval("minute")
		lines := []string{
			`2024-12-30T10:46:01.000000000Z I1230 10:46:01.000000  1 a.go:1] msg1`,
			`2024-12-30T10:46:02.000000000Z I1230 10:46:02.000000  1 a.go:1] msg2`,
			`2024-12-30T10:46:03.000000000Z I1230 10:46:03.000000  1 a.go:1] msg3`,
		}
		for _, l := range lines {
			p.parseLine(l)
		}
		tl := p.Timeline()
		if len(tl) != 1 {
			t.Fatalf("expected 1 bin, got %d", len(tl))
		}
		if tl[0].Count != 3 {
			t.Errorf("bin count = %d, want 3", tl[0].Count)
		}
	})

	t.Run("multi source same bin", func(t *testing.T) {
		p, _ := New(5, 1, 0)
		p.SetTimelineInterval("minute")
		lines := []string{
			`2024-12-30T10:46:01.000000000Z I1230 10:46:01.000000  1 a.go:1] msg`,
			`2024-12-30T10:46:02.000000000Z I1230 10:46:02.000000  1 a.go:1] msg`,
			`2024-12-30T10:46:03.000000000Z I1230 10:46:03.000000  1 b.go:1] msg`,
		}
		for _, l := range lines {
			p.parseLine(l)
		}
		tl := p.Timeline()
		if len(tl) != 1 {
			t.Fatalf("expected 1 bin, got %d", len(tl))
		}
		// bin count must be sum of all sources: 2 + 1 = 3
		if tl[0].Count != 3 {
			t.Errorf("bin count = %d, want 3 (sum across sources)", tl[0].Count)
		}
	})

	t.Run("multiple bins independent counts", func(t *testing.T) {
		p, _ := New(5, 1, 0)
		p.SetTimelineInterval("minute")
		lines := []string{
			`2024-12-30T10:46:01.000000000Z I1230 10:46:01.000000  1 a.go:1] msg`,
			`2024-12-30T10:46:02.000000000Z I1230 10:46:02.000000  1 b.go:1] msg`,
			`2024-12-30T10:47:01.000000000Z I1230 10:47:01.000000  1 a.go:1] msg`,
			`2024-12-30T10:47:02.000000000Z I1230 10:47:02.000000  1 a.go:1] msg`,
			`2024-12-30T10:47:03.000000000Z I1230 10:47:03.000000  1 a.go:1] msg`,
		}
		for _, l := range lines {
			p.parseLine(l)
		}
		tl := p.Timeline()
		if len(tl) != 2 {
			t.Fatalf("expected 2 bins, got %d", len(tl))
		}
		if tl[0].Interval != "2024-12-30T10:46" || tl[0].Count != 2 {
			t.Errorf("bin[0] {%q, count=%d}, want {2024-12-30T10:46, count=2}", tl[0].Interval, tl[0].Count)
		}
		if tl[1].Interval != "2024-12-30T10:47" || tl[1].Count != 3 {
			t.Errorf("bin[1] {%q, count=%d}, want {2024-12-30T10:47, count=3}", tl[1].Interval, tl[1].Count)
		}
	})
}

func TestJSONWithoutTimestampPrefixParsedAsEntry(t *testing.T) {
	p, _ := New(5, 1, 0)
	// JSON log without an RFC3339 timestamp prefix should be parsed as a
	// structured entry (using an empty inherited timestamp), not rejected.
	line := `{"level":"info","ts":"2026-05-07T08:12:52.589790Z","caller":"mvcc/hash.go:151","msg":"storing new hash"}`
	r := strings.NewReader(line + "\n")
	if err := p.Consume(r); err != nil {
		t.Fatalf("Consume: unexpected error: %v", err)
	}
	summary := p.Summary()
	if got := len(summary["info"]); got != 1 {
		t.Errorf("info entries = %d, want 1", got)
	}
}

// Tests derived from openshift-ovn-kubernetes_ovnkube-node-hsc76_ovnkube-controller.log

func TestRFC3339TimezoneOffsetTimestamp(t *testing.T) {
	// Log lines with RFC3339 timestamp (no nanoseconds, +HH:MM timezone offset).
	p, _ := New(5, 1, 0)
	line := `2026-05-27T15:04:35+00:00 [{cnibincopy}] Copying /usr/libexec/cni/ovn-k8s-cni-overlay to /cni-bin-dir/`
	if err := p.parseLine(line); err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	summary := p.Summary()
	if got := len(summary["unstructured"]); got != 1 {
		t.Errorf("unstructured entries = %d, want 1", got)
	}
	if got := summary["unstructured"][0].First[0].Time; got != "2026-05-27T15:04:35+00:00" {
		t.Errorf("timestamp = %q, want 2026-05-27T15:04:35+00:00", got)
	}
}

func TestKlogLineWithoutRFC3339Prefix(t *testing.T) {
	// klog lines that have no RFC3339 timestamp prefix (bare klog output).
	p, _ := New(5, 1, 0)
	// Feed a timestamped line first so lastTimestamp is set.
	p.parseLine(`2026-05-27T15:04:36+00:00 [{setup}] setting up`)
	line := `I0527 15:04:36.622646    3694 cert_rotation.go:141] "Starting client certificate rotation controller"`
	if err := p.parseLine(line); err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	summary := p.Summary()
	infos := summary["info"]
	if len(infos) != 1 {
		t.Fatalf("info entries = %d, want 1", len(infos))
	}
	if infos[0].Source != "cert_rotation.go:141" {
		t.Errorf("source = %q, want cert_rotation.go:141", infos[0].Source)
	}
	// Timestamp derived from klog MMDD+time fields, not inherited from prior line.
	if got := infos[0].First[0].Time; got != "2026-05-27T15:04:36.622646Z" {
		t.Errorf("derived timestamp = %q, want 2026-05-27T15:04:36.622646Z", got)
	}
}

func TestParseOVSLine(t *testing.T) {
	p, _ := New(5, 1, 0)
	line := `2026-03-11T11:44:42Z |  8  | reconnect | INFO | unix:/var/run/openvswitch/db.sock: connecting...`
	p.parseLine(line)

	summary := p.Summary()
	infos := summary["info"]
	if len(infos) != 1 {
		t.Fatalf("info entries = %d, want 1", len(infos))
	}
	// Source is the normalized message, not the module name.
	if infos[0].Source != "unix:/var/run/openvswitch/db.sock: connecting..." {
		t.Errorf("source = %q, want message text", infos[0].Source)
	}
	msg, ok := infos[0].Recent[0].Log.(string)
	if !ok || msg != "unix:/var/run/openvswitch/db.sock: connecting..." {
		t.Errorf("log = %q, want OVS message text", msg)
	}
	if infos[0].Recent[0].Time != "2026-03-11T11:44:42Z" {
		t.Errorf("time = %q, want 2026-03-11T11:44:42Z", infos[0].Recent[0].Time)
	}
}

func TestOVSLineSeverities(t *testing.T) {
	cases := []struct {
		level string
		want  string
	}{
		{"INFO", "info"},
		{"WARN", "warning"},
		{"ERR", "error"},
		{"EMER", "fatal"},
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			p, _ := New(5, 1, 0)
			line := `2026-03-11T11:44:42Z | 10 | mymod | ` + tc.level + ` | test message`
			p.parseLine(line)
			if got := len(p.Summary()[tc.want]); got != 1 {
				t.Errorf("severity %q: got %d entries in bucket %q, want 1", tc.level, got, tc.want)
			}
		})
	}
}

func TestOVSLineUnknownSeverity(t *testing.T) {
	p, _ := New(5, 1, 0)
	line := `2026-03-11T11:44:42Z | 10 | mymod | DBG | debug message`
	p.parseLine(line)

	summary := p.Summary()
	if got := len(summary["unstructured"]); got != 1 {
		t.Fatalf("unstructured entries = %d, want 1", got)
	}
	// Source is the normalized message, not the module.
	if summary["unstructured"][0].Source != "debug message" {
		t.Errorf("source = %q, want \"debug message\"", summary["unstructured"][0].Source)
	}
}

func TestOVSLineWithoutTimestamp(t *testing.T) {
	p, _ := New(5, 1, 0)
	// Same message — both should aggregate under the same source key.
	p.parseLine(`2026-03-11T11:44:42Z |  8  | reconnect | INFO | same message`)
	p.parseLine(`| 10 | reconnect | INFO | same message`)

	summary := p.Summary()
	infos := summary["info"]
	if len(infos) != 1 {
		t.Fatalf("info entries = %d, want 1", len(infos))
	}
	if infos[0].Occurrences != 2 {
		t.Errorf("occurrences = %d, want 2", infos[0].Occurrences)
	}
	if infos[0].Recent[1].Time != "2026-03-11T11:44:42Z" {
		t.Errorf("inherited time = %q, want 2026-03-11T11:44:42Z", infos[0].Recent[1].Time)
	}
}

func TestOVSMessageNormalization(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{
			"Tunnel ovn-58c153-0 appeared in OVSDB",
			"Tunnel ovn-XXXXXX-0 appeared in OVSDB",
		},
		{
			"Connection ovn-e8c80c-0-out-1 went down, bringing it up",
			"Connection ovn-XXXXXX-0-out-1 went down, bringing it up",
		},
		{
			"ovn-e8c80c-0-out-1 is defunct, removing",
			"ovn-XXXXXX-0-out-1 is defunct, removing",
		},
		{
			"Starting ipsec connection ovn-7fdd40-0-out-1",
			"Starting ipsec connection ovn-XXXXXX-0-out-1",
		},
		{
			"Connections for all(125) configured tunnels are Up.",
			"Connections for all(N) configured tunnels are Up.",
		},
		{
			"Refreshing LibreSwan configuration",
			"Refreshing LibreSwan configuration",
		},
		{
			"unix:/var/run/openvswitch/db.sock: connecting...",
			"unix:/var/run/openvswitch/db.sock: connecting...",
		},
	}
	for _, tc := range cases {
		got := normalizeOVSMessage(tc.input)
		if got != tc.want {
			t.Errorf("normalizeOVSMessage(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestOVSLogFileIntegration(t *testing.T) {
	p, _ := New(5, 1, 0)
	// Representative sample from openshift-ovn-kubernetes_ovn-ipsec-host-l4cd5_ovn-ipsec.log
	lines := []string{
		// reconnect module lines (no tunnel IDs)
		`2026-03-11T11:44:42Z |  8  | reconnect | INFO | unix:/var/run/openvswitch/db.sock: connecting...`,
		`2026-03-11T11:44:42Z |  12 | reconnect | INFO | unix:/var/run/openvswitch/db.sock: connected`,
		// tunnel appearances — different IDs should collapse to same source
		`2026-03-11T11:44:42Z |  21 | ovs-monitor-ipsec | INFO | Tunnel ovn-58c153-0 appeared in OVSDB`,
		`2026-03-11T11:44:42Z |  23 | ovs-monitor-ipsec | INFO | Tunnel ovn-f7babc-0 appeared in OVSDB`,
		`2026-03-11T11:44:42Z |  25 | ovs-monitor-ipsec | INFO | Tunnel ovn-ec7044-0 appeared in OVSDB`,
		// connection events
		`2026-03-11T11:44:43Z | 277 | ovs-monitor-ipsec | INFO | Connection ovn-e8c80c-0-out-1 went down, bringing it up`,
		`2026-03-11T11:44:43Z | 279 | ovs-monitor-ipsec | INFO | Connection ovn-e8c80c-0-in-1 went down, bringing it up`,
		// static messages
		`2026-03-11T11:44:43Z | 273 | ovs-monitor-ipsec | INFO | Refreshing LibreSwan configuration`,
		`2026-03-11T11:44:43Z | 293 | ovs-monitor-ipsec | INFO | Refreshing is done.`,
		// count param
		`2026-03-11T11:45:13Z | 315 | ovs-monitor-ipsec | INFO | Connections for all(125) configured tunnels are Up.`,
	}
	for _, l := range lines {
		if err := p.parseLine(l); err != nil {
			t.Fatalf("parseLine: %v", err)
		}
	}

	summary := p.Summary()
	infos := summary["info"]

	// Build source→occurrences map for easy assertion.
	bySource := make(map[string]int)
	for _, e := range infos {
		bySource[e.Source] = e.Occurrences
	}

	// Three tunnel lines should collapse to one template source.
	tunnelKey := "Tunnel ovn-XXXXXX-0 appeared in OVSDB"
	if bySource[tunnelKey] != 3 {
		t.Errorf("source %q: occurrences = %d, want 3", tunnelKey, bySource[tunnelKey])
	}

	// Connection lines with different IDs but same direction collapse.
	connKey := "Connection ovn-XXXXXX-0-out-1 went down, bringing it up"
	if bySource[connKey] != 1 {
		t.Errorf("source %q: occurrences = %d, want 1", connKey, bySource[connKey])
	}

	// Static message used verbatim.
	if bySource["Refreshing LibreSwan configuration"] != 1 {
		t.Errorf("static source occurrences = %d, want 1", bySource["Refreshing LibreSwan configuration"])
	}

	// Paren-number param normalized.
	countKey := "Connections for all(N) configured tunnels are Up."
	if bySource[countKey] != 1 {
		t.Errorf("source %q: occurrences = %d, want 1", countKey, bySource[countKey])
	}

	// Raw messages preserved in payloads (not normalized).
	for _, e := range infos {
		if e.Source == tunnelKey {
			for _, occ := range e.Recent {
				msg, ok := occ.Log.(string)
				if !ok {
					t.Errorf("tunnel log payload is not string: %T", occ.Log)
					continue
				}
				if msg == tunnelKey {
					t.Errorf("tunnel log payload should be raw (with actual ID), got normalized key: %q", msg)
				}
			}
		}
	}
}

func TestParseJournalLine(t *testing.T) {
	p, _ := New(5, 1, 0)
	line := `Mar 11 11:34:49.808770 localhost kernel: Linux version 5.14.0`
	if err := p.parseLine(line); err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	summary := p.Summary()
	infos := summary["info"]
	if len(infos) != 1 {
		t.Fatalf("info entries = %d, want 1", len(infos))
	}
	if infos[0].Source != "kernel" {
		t.Errorf("source = %q, want kernel", infos[0].Source)
	}
	msg, ok := infos[0].Recent[0].Log.(string)
	if !ok || msg != "Linux version 5.14.0" {
		t.Errorf("log = %q, want message text", msg)
	}
	// Timestamp must be a valid RFC3339Nano string.
	if infos[0].Recent[0].Time == "" {
		t.Errorf("time is empty, want RFC3339Nano")
	}
}

func TestParseJournalLineWithPID(t *testing.T) {
	p, _ := New(5, 1, 0)
	line := `Mar 11 11:44:42.123456 ip-10-0-67-28 pluto[2541]: starting pluto`
	if err := p.parseLine(line); err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	summary := p.Summary()
	infos := summary["info"]
	if len(infos) != 1 {
		t.Fatalf("info entries = %d, want 1", len(infos))
	}
	// PID preserved in source key.
	if infos[0].Source != "pluto[2541]" {
		t.Errorf("source = %q, want pluto[2541]", infos[0].Source)
	}
}

func TestJournalTimestampYearInference(t *testing.T) {
	p, _ := New(5, 1, 0)
	// Seed a lastTimestamp with a known year.
	p.lastTimestamp = "2026-03-11T11:34:49Z"
	line := `Mar 11 11:34:49.808770 localhost kernel: boot`
	p.parseLine(line)

	summary := p.Summary()
	infos := summary["info"]
	if len(infos) != 1 {
		t.Fatalf("info entries = %d, want 1", len(infos))
	}
	ts := infos[0].Recent[0].Time
	if !strings.HasPrefix(ts, "2026-") {
		t.Errorf("timestamp = %q, want year 2026 inferred from lastTimestamp", ts)
	}
}

func TestJournalBootSeparatorIsUnstructured(t *testing.T) {
	p, _ := New(5, 1, 0)
	line := `-- Boot 9c0f372855024d50b50626a7546de66d --`
	if err := p.parseLine(line); err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	summary := p.Summary()
	if got := len(summary["unstructured"]); got != 1 {
		t.Errorf("unstructured entries = %d, want 1 (boot separator)", got)
	}
	if got := len(summary["info"]); got != 0 {
		t.Errorf("info entries = %d, want 0", got)
	}
}

func TestJournalLogFileIntegration(t *testing.T) {
	p, _ := New(5, 1, 0)
	lines := []string{
		`-- Boot 9c0f372855024d50b50626a7546de66d --`,
		`Mar 11 11:34:49.808770 localhost kernel: Linux version 5.14.0`,
		`Mar 11 11:34:49.808794 localhost kernel: Command line: BOOT_IMAGE=...`,
		`Mar 11 11:34:49.808810 localhost kernel: BIOS-provided physical RAM map`,
		`Mar 11 11:44:42.000012 ip-10-0-67-28 pluto[2541]: starting pluto`,
		`Mar 11 11:44:42.000064 ip-10-0-67-28 pluto[2541]: ike_alg_register: Registered IKEv1 IKE algorithm`,
		`Mar 11 11:44:43.000100 ip-10-0-67-28 systemd[1]: Started ipsec.service`,
	}
	for _, l := range lines {
		if err := p.parseLine(l); err != nil {
			t.Fatalf("parseLine(%q): %v", l, err)
		}
	}

	summary := p.Summary()

	// Boot separator → unstructured.
	if got := len(summary["unstructured"]); got != 1 {
		t.Errorf("unstructured sources = %d, want 1 (boot separator)", got)
	}

	bySource := make(map[string]int)
	for _, e := range summary["info"] {
		bySource[e.Source] = e.Occurrences
	}

	// Three kernel lines collapse to one source.
	if bySource["kernel"] != 3 {
		t.Errorf("kernel occurrences = %d, want 3", bySource["kernel"])
	}
	// Two pluto[2541] lines.
	if bySource["pluto[2541]"] != 2 {
		t.Errorf("pluto[2541] occurrences = %d, want 2", bySource["pluto[2541]"])
	}
	// One systemd[1] line.
	if bySource["systemd[1]"] != 1 {
		t.Errorf("systemd[1] occurrences = %d, want 1", bySource["systemd[1]"])
	}
}

func TestShellTraceLineInheritsTimestamp(t *testing.T) {
	// Shell xtrace lines (++ cmd) have no timestamp and should inherit the
	// last seen timestamp and be captured as unstructured.
	p, _ := New(5, 1, 0)
	p.parseLine(`2026-05-27T15:04:35+00:00 [{init}] starting`)
	if err := p.parseLine(`++ K8S_NODE=ip-10-0-67-181.us-west-2.compute.internal`); err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	summary := p.Summary()
	unstruct := summary["unstructured"]
	// Two unstructured entries: the [{init}] line and the ++ line.
	if len(unstruct) != 1 {
		t.Fatalf("unstructured entries = %d, want 1", len(unstruct))
	}
	if got := unstruct[0].First[0].Time; got != "2026-05-27T15:04:35+00:00" {
		t.Errorf("inherited timestamp = %q, want 2026-05-27T15:04:35+00:00", got)
	}
}
