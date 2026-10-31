# Migrations

Migration numbers collide across forks. Upstream is at `00020`; forks in circulation add
`00023` and `00033`; this tree adds its own, so `ls migrations/` for the current
high-water mark.

- Renumber anything cherry-picked to the next free slot.
- Keep every number that has already been applied to a live database.

```bash
task create-migration NAME=add_my_thing   # goose -s create, in ./migrations
task migrate                              # needs a running PostgreSQL; docker-compose.yml brings one up
```

`task create-migration` shells out to a `goose` binary, which the Nix shell does **not**
provide — install it separately. `task migrate` needs no binary; it runs goose as a
library through `./internal/dev`.

Run `task gen-gorm` afterwards when the change also touches `internal/model`.

How migrations are embedded and applied: [architecture/data.md](../architecture/data.md#migrations).
