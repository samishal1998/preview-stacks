package cli

// `pstack migrate` — copies deployments and their state from one host to another.

const migrateUsage = "usage: pstack migrate --from <API URL> --to <API URL> [<id>...] [--sleep-source]"

func migrateCmd(args *Parsed, ios IO) *Exit {
	return fail(migrateUsage)
}
