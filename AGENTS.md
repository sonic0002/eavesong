# Working on Eavesong

Eavesong is a self-hosted Go service that people upgrade in place: they replace
the binary or the container image and keep their SQLite file, environment and
saved settings. Every change must keep an existing installation working
exactly as before, unless the change says otherwise and explains how people
move over.

## Backward compatibility

An upgraded installation must start, keep all its data, and behave the same
for every feature that already exists.

### Database (`internal/store`)

- Schema changes are additive only. Add tables with `CREATE TABLE IF NOT
  EXISTS`, and add columns the way `addColumns` and `migrateV03` do: check
  `pragma_table_info`, then `ALTER TABLE ... ADD COLUMN` with a `NOT NULL
  DEFAULT` so existing rows stay valid.
- Never rename or drop a table or column, or change what a stored value means.
  If a value must change shape, add a new column and keep reading the old one.
- JSON stored in a column (such as `projects.drafting`) may gain fields, never
  lose or rename them. A missing field must mean the old behaviour.
- Migrations run on every start, so they must be idempotent and safe on a
  database from any earlier release.

### Identifiers that are saved or deduplicated

These strings are stored, so changing them duplicates or orphans data:

- Source names (`Item.Source`, such as `"hackernews"`, `"devto"`) and the
  format of each source's `ExternalID`. Mentions are deduplicated on them.
- Platform keys in `config.AccountPlatforms` and `Account.Key()` values
  (`"bluesky:acme.dev"`), saved on connections and mentions.
- Settings keys in the `settings` table, such as `notify_weekly` and
  `setup_stage`.
- Notification event keys (`"weekly:<project>:<day>"`, `"sitemap:..."`).
  Changing their format sends every notification again.
- Recommendation keys from `insight.Key`, which saved done/dismissed choices
  refer to.

### Secrets (`internal/secret`)

- Saved secrets use the `v1:` format and must keep opening. A new format gets
  a new prefix, and `Open` keeps reading every older one.
- Never log, export or show a secret in full.

### Configuration and interfaces

- Environment variables keep their names and defaults (`EAVESONG_ADDR` stays
  on localhost, a password stays required beyond it). To rename one, read the
  old name too.
- Credentials saved by older versions are carried forward in
  `Manager.Migrate`; extend it rather than dropping old settings.
- Settings export files from older versions must still import. New export
  fields use `omitempty` and are optional on import.
- Existing URLs and form fields keep working, since people bookmark pages and
  notifications link to them. Add routes; don't move them.
- The generic webhook payload may gain fields, never lose or rename them.

### Behaviour

- A new source or feature is off, or harmless, until someone turns it on or
  connects an account. It must not change which mentions existing projects
  collect or how they are classified.
- Changing a default (intervals, budgets, the X daily cap) counts as a
  behaviour change: say so in the README and the release notes.

## Principles that do not change

- Read only. Eavesong never posts, replies, likes or votes; drafts stay drafts.
- Only official APIs and public feeds. No scraping, no working around limits.
  A place without one is left out until it has one.
- Paced or paid APIs implement `source.Paced` or use a `source.Budget`.

## Before finishing a change

1. `gofmt -l .` prints nothing, and `go vet ./...` and `go test ./...` pass.
2. A change to the store has a test that opens a database in the older shape
   and checks the data survives the upgrade.
3. A new connector has a test against a fake server (see
   `internal/source/*_test.go`).
4. A new source is wired everywhere it appears: `config.AccountPlatforms`,
   `config.Sources`, the verifier, `web/view.go` names and badges, the
   Analytics referrer list in `internal/ga`, `.env.example` if it reads an
   environment variable, and the README's Sources table.
5. `docs/roadmap.md` is updated (it is kept out of Git).

## Style

Write code that reads like the code around it: the same naming, comment
density and error wording. User-facing text is plain and short, and says what
to do next.
