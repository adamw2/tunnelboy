package tunnel

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// UnmanagedSession is a live SSM port-forwarding session — a
// session-manager-plugin process holding a local listening port — that isn't
// tracked in TunnelBoy's own state. Either started outside TunnelBoy (a raw
// `aws ssm start-session`, a script, another tool) or orphaned after its
// owning TunnelBoy process died without running cleanup.
type UnmanagedSession struct {
	PID       int
	LocalPort int
	// Detail is a best-effort description of the target, parsed from the
	// parent process's command line when it's a recognizable
	// `aws ssm start-session ... --parameters {...}` invocation. Empty when
	// the parent can't be identified (e.g. reparented to init after the
	// owning process died).
	Detail string
}

// DiscoverUnmanagedSessions lists session-manager-plugin processes not
// parented by one of managedOwnerPIDs (the PIDs recorded in TunnelBoy's own
// tunnel state — the process that started each tracked tunnel's SSM session
// directly, so a managed session's plugin child is always its immediate
// child). Best-effort: relies on `lsof`/`ps` being present; a missing tool or
// restricted permissions just yields no results, never an error.
func DiscoverUnmanagedSessions(managedOwnerPIDs map[int]bool) []UnmanagedSession {
	out, err := exec.Command("lsof", "-nP", "-iTCP", "-sTCP:LISTEN").Output() // #nosec G204 -- fixed binary and args
	if err != nil {
		return nil
	}

	var sessions []UnmanagedSession
	seen := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "session-m") {
			continue // not a session-manager-plugin row ("session-m..." — lsof truncates COMMAND)
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || seen[pid] {
			continue
		}
		seen[pid] = true

		ppid, ok := parentPID(pid)
		if ok && managedOwnerPIDs[ppid] {
			continue // this plugin process belongs to a tracked tunnel
		}

		port := portFromAddress(fields[len(fields)-2])
		if port == 0 {
			continue
		}
		sessions = append(sessions, UnmanagedSession{
			PID:       pid,
			LocalPort: port,
			Detail:    describeSSMParent(ppid, ok),
		})
	}
	return sessions
}

// parentPID returns pid's parent PID via `ps`, or ok=false if it couldn't be
// read (process already gone, ps missing, etc.).
func parentPID(pid int) (int, bool) {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output() // #nosec G204 -- fixed binary, numeric arg
	if err != nil {
		return 0, false
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false
	}
	return ppid, true
}

// portFromAddress extracts the port from an lsof NAME column like
// "127.0.0.1:3307" or "[::1]:3307".
func portFromAddress(addr string) int {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0
	}
	port, err := strconv.Atoi(addr[i+1:])
	if err != nil {
		return 0
	}
	return port
}

// describeSSMParent best-effort extracts "host:port (profile X)" from ppid's
// command line, when it's a recognizable
// `aws ssm start-session ... --parameters {...}` invocation.
func describeSSMParent(ppid int, ok bool) string {
	if !ok || ppid <= 1 {
		return ""
	}
	out, err := exec.Command("ps", "-ww", "-o", "command=", "-p", strconv.Itoa(ppid)).Output() // #nosec G204 -- fixed binary, numeric arg
	if err != nil {
		return ""
	}
	return parseSSMCommand(string(out))
}

var ssmProfileRE = regexp.MustCompile(`--profile\s+(\S+)`)

// parseSSMCommand extracts a short description from an
// `aws ssm start-session ... --parameters {"host":[...],"portNumber":[...]}`
// command line. Returns "" if it doesn't match that shape.
func parseSSMCommand(cmd string) string {
	const marker = "--parameters"
	i := strings.Index(cmd, marker)
	if i < 0 {
		return ""
	}
	obj := extractJSONObject(cmd[i+len(marker):])
	if obj == "" {
		return ""
	}
	var params struct {
		Host       []string `json:"host"`
		PortNumber []string `json:"portNumber"`
	}
	if err := json.Unmarshal([]byte(obj), &params); err != nil {
		return ""
	}

	detail := ""
	if len(params.Host) > 0 {
		detail = params.Host[0]
		if len(params.PortNumber) > 0 {
			detail += ":" + params.PortNumber[0]
		}
	}
	if m := ssmProfileRE.FindStringSubmatch(cmd); len(m) == 2 {
		if detail != "" {
			detail += " (profile " + m[1] + ")"
		} else {
			detail = "profile " + m[1]
		}
	}
	return detail
}

// extractJSONObject returns the first balanced {...} object in s, or "" if
// none is found.
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
