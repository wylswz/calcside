package calcside.hooks

# Deny any fs write/append under /work/secrets.
deny contains msg if {
	input.phase == "before"
	input.capability == "fs"
	input.op in {"write", "append", "mkdir", "delete"}
	startswith(input.args.path, "/work/secrets")
	msg := sprintf("writes under /work/secrets are forbidden (%s)", [input.args.path])
}
