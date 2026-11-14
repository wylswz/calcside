package calcside.hooks

# In the after phase, deny net responses larger than 1MiB (meta carries
# byte counts, never the body).
deny contains msg if {
	input.phase == "after"
	input.capability == "net"
	input.result.meta.bytes > 1048576
	msg := sprintf("response too large: %d bytes", [input.result.meta.bytes])
}
