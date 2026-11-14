package calcside.hooks

# Block network calls to hosts on this denylist, even when the instance
# allowlist would permit them.
deny contains msg if {
	input.phase == "before"
	input.capability == "net"
	input.args.host == blocked[_]
	msg := sprintf("host %q is denylisted", [input.args.host])
}

blocked := ["malware.example.com", "exfil.bad.example"]
