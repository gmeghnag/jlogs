// Package parser ingests log lines and aggregates them into a per-severity,
// per-source summary suitable for JSON serialization.
//
// Two log formats are recognized:
//
//  1. klog-style lines, e.g.:
//     2024-12-30T10:46:29.390512670Z I1230 10:46:29.390512  1 node_controller.go:1056] No nodes available
//
//  2. JSON-structured lines (the JSON object follows a 30-byte timestamp +
//     space prefix), e.g.:
//     2024-12-30T10:46:29.390512670Z {"level":"info","caller":"foo.go:42","msg":"..."}
//
// Lines that match neither format, or that contain malformed JSON, are
// silently skipped — log parsers must not crash on a single bad line.
package parser

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Severity is the canonical severity bucket for an entry. Using a typed string
// instead of a bare string prevents typos at compile time and gives us a
// single source of truth for valid values.
type Severity string

const (
	SevInfo         Severity = "info"
	SevWarning      Severity = "warning"
	SevError        Severity = "error"
	SevFatal        Severity = "fatal"
	SevUnstructured Severity = "unstructured"
)

// trackedSeverities is the ordered set of severities we aggregate. Order is
// preserved in the output for stable, human-friendly diffs.
var trackedSeverities = []Severity{SevInfo, SevWarning, SevError, SevFatal, SevUnstructured}

// Occurrence is a single retained log event for a given source.
type Occurrence struct {
	Time string `json:"time"`
	// Log holds either a string (klog message) or a parsed JSON object
	// (structured log). interface{} is appropriate here because the original
	// Python output mixed both shapes; downstream consumers already handle it.
	Log any `json:"log"`
}

// SourceSummary is the per-source aggregation emitted in the final report.
type SourceSummary struct {
	Source      string       `json:"source_filename_linenumber"`
	Occurrences int          `json:"occurrences"`
	Frequency   string       `json:"frequency,omitempty"` // e.g., "10/s", "5/m", "4/h"
	Recent      []Occurrence `json:"-"`                   // rendered under "last_occurrences" key; see MarshalJSON
	First       []Occurrence `json:"-"`                   // rendered under "first_occurrences" key; see MarshalJSON
	recentKey   string       // "last_occurrences"
	firstKey    string       // "first_occurrences"
}

// MarshalJSON renders SourceSummary with the "last_occurrences" and "first_occurrences" keys.
func (s SourceSummary) MarshalJSON() ([]byte, error) {
	// Build a map so we can place the dynamic keys alongside the static fields.
	out := map[string]any{
		"source_filename_linenumber": s.Source,
		"occurrences":                s.Occurrences,
		s.firstKey:                   s.First,
		s.recentKey:                  s.Recent,
	}
	if s.Frequency != "" {
		out["frequency"] = s.Frequency
	}
	return jsonMarshal(out)
}

// klogPattern matches a klog-formatted log line after the timestamp prefix
// has already been stripped by extractTimestamp.
//
//	1: klog level + date (e.g. I1230)
//	2: klog wall-clock time (e.g. 10:46:29.390512)
//	3: source file:line  (e.g. node_controller.go:1056)
//	4: message           (rest of line)
var klogPattern = regexp.MustCompile(`([IWEF]\d{4})\s+(\S+)\s+\d+\s+(\S+\.go:\d+)\]\s+(.*)`)

// klogLevelToSeverity maps the single-letter klog prefix to our canonical
// severity. Anything not in this map is unknown and dropped.
var klogLevelToSeverity = map[byte]Severity{
	'I': SevInfo,
	'W': SevWarning,
	'E': SevError,
	'F': SevFatal,
}

// ovsPattern matches OVS pipe-delimited log lines after the timestamp prefix
// has been stripped: | SEQ | MODULE | LEVEL | MESSAGE
var ovsPattern = regexp.MustCompile(`\|\s*\d+\s*\|\s*(\S+)\s*\|\s*(\w+)\s*\|\s*(.*)`)

// ovsHexID matches OVN tunnel/connection hex identifiers, e.g. ovn-58c153-
var ovsHexID = regexp.MustCompile(`ovn-[0-9a-f]{4,}-`)

// ovsParenNum matches parenthesized numbers, e.g. (125)
var ovsParenNum = regexp.MustCompile(`\(\d+\)`)

// ovsLevelToSeverity maps OVS severity strings to canonical severities.
var ovsLevelToSeverity = map[string]Severity{
	"INFO": SevInfo,
	"WARN": SevWarning,
	"ERR":  SevError,
	"EMER": SevFatal,
}

// jsonLevelToSeverity maps the strings that may appear in a structured log's
// "level" or "severity" field to our canonical severity.
var jsonLevelToSeverity = map[string]Severity{
	"info":    SevInfo,
	"warn":    SevWarning,
	"warning": SevWarning,
	"error":   SevError,
	"fatal":   SevFatal,
}

// Parser accumulates parsed log entries. Construct with New, feed lines via
// Consume, then read the result with Summary. A Parser is not safe for
// concurrent use; create one per goroutine if you need parallelism.
type Parser struct {
	lastN         int
	firstN        int
	recentKey     string
	firstKey      string
	since         time.Duration // filter logs to last N duration from latest timestamp
	latestTime    time.Time     // latest timestamp seen during parsing
	hasLatestTime bool          // whether we've seen any valid timestamps
	lastTimestamp string        // most-recently-seen timestamp for timestamp-less lines
	// buckets[severity][source] -> aggregator
	buckets map[Severity]map[string]*sourceAggregator

	// timeline fields; populated only when SetTimelineInterval is called
	timelineInterval string // "hour", "minute", or "second"
	timelineBuckets  map[string]map[string]*timelineSourceAgg // intervalKey -> source -> agg
}

// timelineSourceAgg holds the count and one sample message for an (interval, source) pair.
type timelineSourceAgg struct {
	count  int
	sample any
}

// SetTimelineInterval enables timeline mode with the given granularity.
// interval must be "hour", "minute", or "second"; anything else is treated as "minute".
func (p *Parser) SetTimelineInterval(interval string) {
	switch interval {
	case "hour", "minute", "second":
		p.timelineInterval = interval
	default:
		p.timelineInterval = "minute"
	}
	p.timelineBuckets = make(map[string]map[string]*timelineSourceAgg)
}

// sourceAggregator tracks running counts, a fixed-size ring of recent
// occurrences, and the first N occurrences for a single (severity, source) pair.
type sourceAggregator struct {
	occurrences    int
	recent         *ring
	first          []Occurrence
	firstCap       int
	firstTimestamp string   // timestamp of the first occurrence
	lastTimestamp  string   // timestamp of the most recent occurrence
	allTimestamps  []string // all timestamps for accurate --since filtering
}

// New constructs a Parser. lastN and firstN must be in the range [1, 10];
// values outside that range fall back to 5 and 1 respectively.
// since filters logs to only include those from the last N duration relative to the latest log.
func New(lastN, firstN int, since time.Duration) (*Parser, error) {
	if lastN < 1 || lastN > 10 {
		lastN = 5
	}
	if firstN < 1 || firstN > 10 {
		firstN = 1
	}
	p := &Parser{
		lastN:     lastN,
		firstN:    firstN,
		since:     since,
		recentKey: "last_occurrences",
		firstKey:  "first_occurrences",
		buckets:   make(map[Severity]map[string]*sourceAggregator, len(trackedSeverities)),
	}
	for _, sev := range trackedSeverities {
		p.buckets[sev] = make(map[string]*sourceAggregator)
	}
	return p, nil
}

// Consume reads r line-by-line until EOF, parsing and aggregating each line.
// Returns an error if any line has an invalid timestamp prefix or if I/O fails.
// We use a Scanner with a generous buffer because real-world logs occasionally
// contain very long lines (stack traces, JSON blobs).
func (p *Parser) Consume(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	// Default Scanner buffer is 64KB which is too small for long stack traces.
	// 1MB handles the vast majority of real log lines without unbounded growth.
	const maxLine = 1024 * 1024
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if line == "" {
			continue
		}
		if err := p.parseLine(line); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	return nil
}

// calculateFrequency computes the occurrence rate between first and last timestamps.
// Returns actual occurrences per time unit (not extrapolated), e.g., "5.0/m", "0.7/h", "2.5/d".
// Unit is chosen based on time span duration, and rates can be fractional for all units.
// If there's only one occurrence or timestamps can't be parsed, returns empty string.
// Requires a minimum duration of 1 second to avoid misleading rates from startup bursts.
func calculateFrequency(occurrences int, firstTS, lastTS string) string {
	if occurrences <= 1 || firstTS == "" || lastTS == "" {
		return ""
	}

	first, err := time.Parse(time.RFC3339Nano, firstTS)
	if err != nil {
		return ""
	}
	last, err := time.Parse(time.RFC3339Nano, lastTS)
	if err != nil {
		return ""
	}

	duration := last.Sub(first).Seconds()

	// Require at least 1 second of duration to avoid misleading frequencies
	// from startup bursts (e.g., 57 logs in 4ms)
	if duration < 1.0 {
		return ""
	}

	// Choose unit based on duration (not rate) and show actual occurrences per that unit
	// This avoids extrapolation: 5 logs in 7 minutes shows as "0.7/m", not "43/h"

	if duration < 60 {
		// Less than 1 minute: use seconds
		rate := float64(occurrences) / duration
		if rate >= 10 {
			return fmt.Sprintf("%.0f/s", rate)
		}
		return fmt.Sprintf("%.1f/s", rate)
	}

	if duration < 3600 {
		// Less than 1 hour: use minutes
		rate := float64(occurrences) / (duration / 60.0)
		if rate >= 10 {
			return fmt.Sprintf("%.0f/m", rate)
		}
		return fmt.Sprintf("%.1f/m", rate)
	}

	if duration < 86400 {
		// Less than 1 day: use hours
		rate := float64(occurrences) / (duration / 3600.0)
		if rate >= 10 {
			return fmt.Sprintf("%.0f/h", rate)
		}
		return fmt.Sprintf("%.1f/h", rate)
	}

	// 1 day or more: use days
	rate := float64(occurrences) / (duration / 86400.0)
	if rate >= 10 {
		return fmt.Sprintf("%.0f/d", rate)
	}
	return fmt.Sprintf("%.1f/d", rate)
}

// addEntry records one parsed log event under the appropriate severity bucket.
func (p *Parser) addEntry(sev Severity, source, timestamp string, message any) {
	bucket, ok := p.buckets[sev]
	if !ok {
		// Severity isn't tracked; drop. This happens for "unknown" levels.
		return
	}

	// Track the latest timestamp seen
	if t, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
		if !p.hasLatestTime || t.After(p.latestTime) {
			p.latestTime = t
			p.hasLatestTime = true
		}
	}

	agg, ok := bucket[source]
	if !ok {
		agg = &sourceAggregator{
			recent:         newRing(p.lastN),
			first:          make([]Occurrence, 0, p.firstN),
			firstCap:       p.firstN,
			firstTimestamp: timestamp,
			allTimestamps:  make([]string, 0),
		}
		bucket[source] = agg
	}
	agg.occurrences++
	agg.lastTimestamp = timestamp
	agg.allTimestamps = append(agg.allTimestamps, timestamp)
	agg.recent.push(Occurrence{Time: timestamp, Log: message})
	// Only collect first N occurrences
	if len(agg.first) < agg.firstCap {
		agg.first = append(agg.first, Occurrence{Time: timestamp, Log: message})
	}

	if p.timelineInterval != "" && timestamp != "" {
		intervalKey := timelineIntervalKey(timestamp, p.timelineInterval)
		sourceBucket, ok := p.timelineBuckets[intervalKey]
		if !ok {
			sourceBucket = make(map[string]*timelineSourceAgg)
			p.timelineBuckets[intervalKey] = sourceBucket
		}
		tagg, ok := sourceBucket[source]
		if !ok {
			tagg = &timelineSourceAgg{}
			sourceBucket[source] = tagg
		}
		tagg.count++
		if tagg.count == 1 {
			tagg.sample = message
		}
	}
}

// timelineIntervalKey truncates a RFC3339Nano timestamp to the requested granularity.
func timelineIntervalKey(timestamp, interval string) string {
	switch interval {
	case "hour":
		if len(timestamp) >= 13 {
			return timestamp[:13]
		}
	case "second":
		if len(timestamp) >= 19 {
			return timestamp[:19]
		}
	}
	// default: minute
	if len(timestamp) >= 16 {
		return timestamp[:16]
	}
	return timestamp
}
