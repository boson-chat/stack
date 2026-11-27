# Stack migrations

One-off changes an existing install needs when it moves to a newer stack
release: a new `.env` key, an entry added to a list, a line in a file setup
writes. The admin panel's Updates page (and `scripts/stack-apply.sh
migrate`) runs the ones an install hasn't run yet, in order, after the new
release's files are in place. Rules: [docs/updates.md](../docs/updates.md#migrations).
