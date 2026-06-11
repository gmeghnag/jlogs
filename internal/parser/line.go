package parser

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// extractTimestamp tries to parse an RFC3339-family timestamp from the start
// of line (the token before the first space). Returns (timestamp, rest, true)
// on success, or ("", line, false) when no valid timestamp is found so the
// caller can inherit p.lastTimestamp.
func (p *Parser) extractTimestamp(line string) (timestamp, rest string, found bool) {
	spaceIdx := strings.IndexByte(line, ' ')
	if spaceIdx <= 0 {
		return "", line, false
	}
	candidate := line[:spaceIdx]
	// RFC3339Nano also accepts plain RFC3339 (no sub-second precision or Z suffix).
	if _, err := time.Parse(time.RFC3339Nano, candidate); err == nil {
		return candidate, line[spaceIdx+1:], true
	}
	return "", line, false
}

// parseLine dispatches a single log line to the appropriate parser.
// If the line carries an RFC3339-family timestamp prefix it is used and
// remembered for subsequent timestamp-less lines. Lines without a
// recognisable timestamp inherit the most-recently-seen one (empty string
// when none has been seen yet) and are never rejected.
func (p *Parser) parseLine(line string) error {
	timestamp, rest, found := p.extractTimestamp(line)
	if found {
		p.lastTimestamp = timestamp
	} else {
		timestamp = p.lastTimestamp
		rest = line
	}

	// Try klog format first — it's the more common shape and the regex is
	// fast on lines that don't match (the leading anchor fails quickly).
	if m := klogPattern.FindStringSubmatch(rest); m != nil {
		// m[1] is e.g. "I1230"; first byte is the level letter.
		level := m[1][0]
		sev, ok := klogLevelToSeverity[level]

		// When the line has no RFC3339 prefix, derive a full timestamp from the
		// klog MMDD+time fields so timeline binning uses the line's own time.
		if !found {
			if ts := klogDerivedTimestamp(m[1], m[2], p.lastTimestamp); ts != "" {
				timestamp = ts
				p.lastTimestamp = ts
			}
		}

		if !ok {
			p.addEntry(SevUnstructured, "unstructured", timestamp, rest)
			return nil
		}
		source := m[3]
		message := m[4]
		p.addEntry(sev, source, timestamp, message)
		return nil
	}

	// Try OVS pipe-delimited format: | SEQ | MODULE | LEVEL | MESSAGE
	if m := ovsPattern.FindStringSubmatch(rest); m != nil {
		source := normalizeOVSMessage(m[3])
		sev, ok := ovsLevelToSeverity[m[2]]
		if !ok {
			p.addEntry(SevUnstructured, source, timestamp, m[3])
			return nil
		}
		p.addEntry(sev, source, timestamp, m[3])
		return nil
	}

	// Try syslog/journal format: Mon DD HH:MM:SS.ffffff HOSTNAME SERVICE[PID]: MESSAGE
	// Only attempted when no RFC3339 prefix was found (rest == line).
	if !found {
		if m := journalPattern.FindStringSubmatch(rest); m != nil {
			ts := journalTimestamp(m[1], p.lastTimestamp)
			if ts != "" {
				p.lastTimestamp = ts
			} else {
				ts = timestamp
			}
			source := strings.TrimSuffix(m[2], ":")
			p.addEntry(SevInfo, source, ts, m[3])
			return nil
		}
	}

	// Try JSON-structured format. Cheap shape check before invoking the
	// JSON decoder, which is comparatively expensive.
	if strings.HasPrefix(rest, "{") && strings.HasSuffix(rest, "}") {
		if p.tryParseJSONLine(timestamp, rest) {
			return nil
		}
	}

	// Catch-all: didn't match any known format, capture as unstructured.
	p.addEntry(SevUnstructured, "unstructured", timestamp, rest)
	return nil
}

// journalTimestamp converts a syslog-style timestamp ("Mar 11 11:34:49.808770")
// to an RFC3339Nano string. Year is taken from lastTimestamp when available
// (first 4 bytes), falling back to the current year.
func journalTimestamp(s, lastTimestamp string) string {
	year := strconv.Itoa(time.Now().Year())
	if len(lastTimestamp) >= 4 {
		year = lastTimestamp[:4]
	}
	// Prepend the year so Go's reference layout can parse it.
	t, err := time.Parse("2006 Jan _2 15:04:05.999999999", year+" "+s)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// normalizeOVSMessage replaces parameterized parts of an OVS log message with
// placeholders so distinct tunnel/connection IDs and counts collapse to the
// same template, which is then used as the aggregation source key.
func normalizeOVSMessage(msg string) string {
	s := ovsHexID.ReplaceAllString(msg, "ovn-XXXXXX-")
	s = ovsParenNum.ReplaceAllString(s, "(N)")
	return strings.TrimSpace(s)
}

// klogDerivedTimestamp builds a UTC RFC3339Nano timestamp from klog's MMDD
// and wall-clock time fields. levelDate is e.g. "I0527", klogTime is e.g.
// "15:04:36.622646". Year is taken from the first 4 chars of lastTimestamp
// when available, falling back to the current year.
func klogDerivedTimestamp(levelDate, klogTime, lastTimestamp string) string {
	if len(levelDate) != 5 {
		return ""
	}
	mmdd := levelDate[1:] // "0527"
	year := strconv.Itoa(time.Now().Year())
	if len(lastTimestamp) >= 4 {
		year = lastTimestamp[:4]
	}
	return year + "-" + mmdd[:2] + "-" + mmdd[2:] + "T" + klogTime + "Z"
}

// jsonLog is a permissive view over the structured-log object. We only pull
// out the fields we need for aggregation; the full object is preserved as the
// occurrence payload.
type jsonLog struct {
	Level    string `json:"level"`
	Severity string `json:"severity"`
	Caller   string `json:"caller"`
	Message  string `json:"message"`
	Msg      string `json:"msg"`
	Error    string `json:"error"`
}

// tryParseJSONLine attempts to parse a JSON-structured log line.
// Returns true if parsing succeeded and entry was added, false otherwise.
func (p *Parser) tryParseJSONLine(timestamp, payload string) bool {
	var meta jsonLog
	if err := json.Unmarshal([]byte(payload), &meta); err != nil {
		// Malformed JSON on a single line is not fatal — return false
		// so caller can capture as unstructured.
		return false
	}

	// Resolve severity: prefer "level", fall back to "severity".
	rawLevel := meta.Level
	if rawLevel == "" {
		rawLevel = meta.Severity
	}
	sev, ok := jsonLevelToSeverity[strings.ToLower(rawLevel)]
	if !ok {
		// Unrecognized severity -> return false for unstructured capture
		return false
	}

	source := meta.Caller
	if source == "" {
		source = "unknown"
	}

	// We also want to preserve the full structured payload as the "log" field,
	// matching the Python output. Decode again into a generic map so JSON
	// re-encoding round-trips cleanly without losing fields we didn't model.
	var full map[string]any
	if err := json.Unmarshal([]byte(payload), &full); err != nil {
		return false
	}

	// Append the error field into the message for visibility, mirroring the
	// Python behavior. We mutate the rendered message only — not the stored
	// full object — so consumers can still see the original "error" key.
	if meta.Error != "" {
		// Compose a message string for any future text-only consumers; the
		// stored payload is the full JSON object regardless.
		base := meta.Message
		if base == "" {
			base = meta.Msg
		}
		_ = base + " - " + meta.Error // currently unused; kept for parity / future use
	}

	p.addEntry(sev, source, timestamp, full)
	return true
}
