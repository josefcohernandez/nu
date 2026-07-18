# Session persistence

Status: **draft for discussion**. Storage contract of the official agent
extension — it is not sacred core API ([api.md](api.md)). It's documented
as a **public convention**: any extension or external tool can read
sessions (pickers, exporters, cost statistics) without going through the
agent.

## 1. Principles

1. **Append-only.** A session is a file to which lines are only ever added,
   never rewritten. Crash-proof (what's written stays written), cheap on
   long sessions (history isn't reserialized on every turn), and trivial to
   follow live (`tail -f`).
2. **State is reconstructed by replay.** Reopening a session = reading the
   file top to bottom applying each entry. There's no second "current
   state" file that could get out of sync.
3. **Reuses the canonical model.** Messages are serialized exactly as
   defined by [providers.md](providers.md) (blocks, `meta` included): a
   resumed session produces requests identical to the original's.
4. **The core only provides `enu.fs` and `enu.json`.** None of this is a
   primitive.

## 2. Location

```
enu.config.data_dir()/
  sessions/
    <project>/                           # cwd encoded as a slug
      2026-06-11T10-22-07Z-a3f9.jsonl    # one session = one file
  plugins/
    <plugin-name>/                        # each plugin's private storage
```

- Grouping **by project** (slug of the `cwd`): "resume the last session of
  this repo" is a directory listing.
- **The slug is part of the format (G38).** Since this contract promises
  reading by external tools (§1), the cwd→directory encoding can't be a
  private detail. Algorithm: every character outside `[A-Za-z0-9.-]` is
  replaced with `_`; `_` characters are trimmed from both edges; if the
  result is empty, `"root"`. Example: `/home/diego/nu` → `home_diego_nu`. It
  is deliberately **readable and lossy**: it's not reversible, and two
  pathologically similar `cwd` values (`/a/b` and `/a_b`) can collide in the
  same directory. It's not an identity but a **grouping key**: each
  session's canonical identity travels *inside* the file (the `meta` line
  carries `cwd` and `id`) — whoever needs to disambiguate a collision reads
  `meta`. So that no plugin reimplements the encoding, the extension
  exposes it as pure functions: `sessions.slug(cwd) -> string` and
  `sessions.dir(cwd) -> string` (`data_dir()/sessions/<slug>`); external
  tools compose it from this specification.
- File name = session id: UTC timestamp + random suffix. Lexicographic
  ordering = temporal ordering.
- `0600` permissions: transcripts contain code and command output, so they
  must not be readable by other users on the machine. Guaranteed by creating
  the file empty with `enu.fs.write(path, "", { exclusive = true, mode = 0600
  })` ([api.md](api.md) §5, G57: `mode` does an explicit chmod **not trimmed by
  the umask**) before the first `append`; since `append` preserves the existing
  file's mode, `0600` is kept on every append. The lockfile (§6) is created
  with the same mode.
- General rule for the other extensions: each plugin only writes under
  `plugins/<its-name>/`. `sessions/` is the only shared convention.

## 3. Format: JSONL entries

One entry per line. Every entry has `t` (type) and activity entries carry
`ts` (epoch ms). v1 types:

```
{ "t": "meta",    "v": 1, "id", "cwd", "created", "parent"? }
{ "t": "message", "ts", "message": Message, "usage"?, "model"? }
{ "t": "compact", "ts", "summary": Message, "covers": integer }
{ "t": "event",   "ts", "ns": string, "data": any }
```

- **`meta`**: always the first line. `v` is the format version.
  `parent? = { id, entry }` links forks (see §5).
- **`message`**: a complete canonical `Message` (role + blocks, with block
  `meta` intact). For `assistant`-role entries, `usage` (the provider's
  event) and `model` are attached: cost and context fill are audited by
  reading the file.
- **`compact`**: compaction doesn't erase history. `summary` is the summary
  message and `covers` is the number of `message` entries it replaces. On
  replay for the LLM: the last `compact` is taken along with the `message`
  entries that follow it; everything before that stays in the file for
  human eyes and tools.
- **`event`**: generic namespaced escape hatch for everything else
  (mid-session model change, title, user marks). Replay rule (G46): for
  repeatable data (e.g. the title or the model change), the last one wins;
  for cumulative data (e.g. the agent's `allow`/`deny`), they're reapplied
  **in order**. `event` entries are reread from the **entire** transcript,
  not from the last `compact` onward (compaction summarizes messages, not
  configuration). Precedence versus the resumer's explicit options is set
  by the consumer's contract (for the agent, [agente.md](agente.md) §2:
  resume opts > `event` > `agent.toml`). Third-party extensions use their
  plugin name as `ns`.

Read robustness: a last line truncated mid-write (crash) is silently
discarded. Lines with an unknown `t` are ignored (forward compatible: newer
versions can add types).

## 4. Streaming and atomicity

Nothing is written during response streaming: the deltas are for the
screen. When the turn completes (the adapter's `done`), **one**
`enu.fs.append` is done with the whole `message` entry. A session never
contains half-finished messages; if the process dies mid-response, the turn
simply doesn't exist (and the request can be relaunched on resume).

## 5. Forks and rewind

Rewinding to an earlier point and trying another path **doesn't mutate the
file** (append-only): it creates a new session whose `meta.parent` points
to the origin session. **The fork copies the prefix into the child's
transcript (G39)**: the child session is **self-contained** — its replay
doesn't follow the parent chain, and its file travels alone (which makes
exporting a fork or moving it between machines trivial: the format is the
API, [P9](../postponed/pospuesto.md)). The cost of duplicating the prefix is irrelevant
against that robustness. `meta.parent = { id, entry }` is **navigational**,
not a replay pointer: it serves to reconstruct the variant tree by reading
the `meta` entries; `entry` is the message index of the parent's current
history at the moment of the fork (the unit of `Session:fork(at)`,
[agente.md](agente.md) §2). The original history remains intact.

## 6. Concurrency: one writer per session (G5)

Two processes appending to the same JSONL = interleaved corruption. Rule:
**a session has at most one writer**, guaranteed by a lockfile.

- `<session>.jsonl.lock` next to the transcript, content
  `{ pid, hostname, started }`. It's acquired on opening for writing
  (create/resume) with **exclusive** creation
  (`enu.fs.write(..., { exclusive = true, mode = 0600 })`, atomic: two
  processes can't win at the same time — [api.md](api.md) §5; `mode` leaves it
  at `0600`, not world-readable, G57), and released on exit. The
  writer identity recorded is that of the current `enu` process: `pid`,
  from `enu.sys.pid()` (G32); `hostname`, from `enu.sys.hostname()` (G17);
  `started`, from `enu.sys.now_ms()`. When *verifying* someone else's lock,
  its `pid` is checked with `enu.proc.alive` (existence on this machine, not
  identity — G17). **Reading never requires a lock** (an append-only file
  is safe to read mid-write).
- **Orphan lock** (crash): if the `pid` isn't alive on this machine, it's
  garbage — cleaned up silently. If the lock belongs to a different
  `hostname` (synced directory), it can't be verified: it's asked about,
  never assumed.
- **Real conflict** (pid alive): the second process gets a clear notice
  with three outcomes — **fork** (default: continues on a new branch via
  `meta.parent`, §5, without stepping on anyone), **read-only**, or
  **force** (steal the lock, explicit and with confirmation).
- Lockfile was chosen over the OS's `flock` for predictable semantics on
  Windows and network filesystems; silent auto-fork was discarded for
  branching history without the user's knowledge.

## 7. Listing and resuming

- Listing a project's sessions = listing `sessions/<project>/` and reading
  the first line (`meta`) and the last relevant one (title/timestamp) of
  each file. No global index in v1: if it ever hurts, a *reconstructible*
  index gets added (a cache, never the source of truth).
- Subagents (whether they run as a task or as a worker): their transcript
  is a session of its own with `meta.parent` pointing to the parent entry
  that launched them — same mechanics as forks, auditable with the same
  tools.

## 8. What's out of scope (v1)

- Encryption at rest and secret redaction in tool results: the transcript
  is faithful; protecting it is the filesystem's job (the `0600` §2 sets at
  creation, G57).
- Cross-machine sync and search indexes: buildable on top by extensions
  (the format is the API).
- Garbage collection of old sessions: the agent extension's policy
  (configurable), not the format's.
