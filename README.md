# jlogs

A fast Go CLI tool that parses and aggregates log lines into a structured JSON summary.

<img src="./jlogs.png" width="100%">

## Purpose

`jlogs` reads log files (or stdin), recognizes klog-style and JSON-structured log formats, groups entries by severity and source location, and outputs a JSON summary with occurrence counts and recent examples.

Supported log formats:
- **klog-style**: `I1230 10:46:29.390512  1 node_controller.go:1056] No nodes available`
- **JSON-structured**: `{"level":"info","caller":"foo.go:42","msg":"..."}`
- **unstructured**: Lines that don't match klog or JSON formats

**Timestamp handling**: Lines may optionally start with an RFC3339 timestamp prefix (e.g. from `kubectl logs --timestamps=true`). When present, it is used; when absent, klog lines derive a timestamp from their own `MMDD HH:MM:SS` fields, and other lines inherit the most recently seen timestamp.

## Installation

### Download the latest binary
```
# cd to a directory that is in your $PATH
curl -sL "https://github.com/gmeghnag/jlogs/releases/latest/download/jlogs_$(uname)_$(uname -m).tar.gz" | tar xzf - jlogs && chmod +x ./jlogs

jlogs -h
```

### Using go install
```bash
go install github.com/gmeghnag/jlogs/cmd/jlogs@latest
```

### Build from source
```bash
git clone https://github.com/gmeghnag/jlogs.git
cd jlogs
go build -o jlogs ./cmd/jlogs
```

## Usage

**Kubernetes/OpenShift logs:**
```bash
# With --timestamps=true (recommended for best timeline accuracy)
oc logs -n openshift-etcd pod-name --timestamps=true | jlogs
kubectl logs pod-name --timestamps=true | jlogs

# Without --timestamps also works (klog timestamps are used automatically)
oc logs pod-name | jlogs
```

**Local files:**
```bash
cat app.log | jlogs
jlogs app.log error.log

# Customize output
jlogs -first-n 2 -last-n 10 -pretty=false app.log
```

**Timeline view:**
```bash
# Group log activity by minute (default)
oc logs pod-name --timestamps=true | jlogs -timeline

# Group by hour or second
jlogs -timeline -interval hour app.log
jlogs -timeline -interval second app.log
```

### Options

#### `-v`
Print version information and exit.
- Displays the git tag and commit hash set at build time
- Example: `jlogs -v` outputs `jlogs version v1.0.0 (commit a1b2c3d)`

#### `-last-n` (default: 5)
Number of most recent occurrences to retain per source in the output.
- Valid range: 1-10
- Values outside this range fall back to default (5)
- Example: `-last-n 3` keeps the 3 most recent log entries for each source

#### `-first-n` (default: 1)
Number of first occurrences to retain per source in the output.
- Valid range: 1-10
- Values outside this range fall back to default (1)
- Useful for capturing initial log entries along with recent ones
- Example: `-first-n 2` keeps the 2 earliest log entries for each source

#### `-since`
Filter logs to include only those from the last N time units, relative to the latest timestamp in the log stream.
- Supported units: `minute(s)`, `hour(s)`, `day(s)`
- Format: `"N unit"` (quotes recommended)
- When applied, recalculates occurrence counts and frequencies based on the filtered time window
- Sources with no occurrences in the time window are omitted from output
- Examples:
  - `-since "1 hour"` - logs from the last hour
  - `-since "30 minutes"` - logs from the last 30 minutes
  - `-since "2 days"` - logs from the last 2 days

#### `-timeline`
Emit a timeline view instead of a summary. Groups log activity by time interval, showing which sources were active and a sample message per source.

Output shape:
```json
[
  {
    "interval": "2024-12-30T10:46",
    "count": 12,
    "sources": [
      { "source": "node_controller.go:1056", "count": 10, "sample": "No nodes available" },
      { "source": "foo.go:42",               "count": 2,  "sample": "connection refused"  }
    ]
  }
]
```
- `interval`: truncated timestamp representing the time window
- `count`: total log lines in the interval across all sources
- `sources`: sorted by count ascending (most active source last)
- `sample`: first log message seen from that source in the interval

#### `-interval` (default: `minute`)
Granularity for `-timeline` mode.
- `hour` — groups by hour, e.g. `2024-12-30T10`
- `minute` — groups by minute, e.g. `2024-12-30T10:46`
- `second` — groups by second, e.g. `2024-12-30T10:46:29`

#### `-pretty` (default: true)
Enable or disable pretty-printed JSON output.
- `true`: indented, human-readable JSON
- `false`: compact, single-line JSON
- Example: `-pretty=false` for compact output

## Output

Produces a JSON summary grouped by severity (info, warning, error, fatal, unstructured) with:
- Source file and line number
- Total occurrence count
- Frequency (occurrences per time period: X/s, X/m, X/h, or X.X/d)
- First N log entries
- Most recent N log entries

**Severity categories:**
- **info, warning, error, fatal**: Recognized klog and JSON log levels
- **unstructured**: Lines with valid timestamp that don't match klog or JSON formats, or have unrecognized severity levels

---

*AI-Assisted Go rewrite of the original Python implementation by Gabriel Meghnagi*
