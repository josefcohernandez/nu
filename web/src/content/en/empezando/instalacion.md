---
title: Installation
description: Install the static enu binary from a release or build it with Go.
---

`enu` is **a single static binary** with no dynamic dependencies
(`CGO_ENABLED=0`): it runs as-is on any distro or container. There's no need
to install Node, npm, or any runtime.

## Quick install (`curl | sh`)

The one-liner path: the script detects your system (linux/darwin ×
amd64/arm64), downloads the binary for the latest release, **verifies the
checksum**, and installs it on your `PATH`.

```sh
curl -fsSL https://raw.githubusercontent.com/dbareagimeno/enu/main/install.sh | sh
```

By default it installs to `~/.local/bin` (or `/usr/local/bin` if you have
permission); you can force the destination with `ENU_INSTALL_DIR`:

```sh
curl -fsSL https://raw.githubusercontent.com/dbareagimeno/enu/main/install.sh | ENU_INSTALL_DIR=/usr/local/bin sh
```

Prefer to review it before running it? Download it, read it, and run it by
hand — it's a short, magic-free POSIX script. If you'd rather skip the
script, use the manual method below.

## From a release (recommended)

Every release publishes the binary for the target platforms (linux/darwin ×
amd64/arm64). Download the `.tar.gz` for your system from the [latest
release](https://github.com/dbareagimeno/enu/releases/latest), unpack it, and
put it on your `PATH`:

```sh
# Adjust VERSION and the platform.
tar -xzf enu-vVERSION-linux-amd64.tar.gz
chmod +x enu
sudo mv enu /usr/local/bin/

enu -e 'return enu.version'   # verify the install (headless, no TTY)
```

Verify integrity with the `checksums.txt` that ships with each release:

```sh
sha256sum -c checksums.txt
```

## Building from source

You need Go (the minimum version is in `go.mod`):

```sh
git clone https://github.com/dbareagimeno/enu
cd enu
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o enu .
```

## Windows

On Windows, `enu` is used via **WSL2** with the `linux/amd64` binary. Native
Windows support is postponed.

## Checking it works

```sh
enu -e 'return enu.version'
```

You should see a table with `major`, `minor`, `patch`, and `api` (the core
API level). If you see it, you already have a working Lua runtime.

:::note[Bare runtime]
A freshly installed `enu` **ships with no extension active**: launching it
with a TTY shows you a runtime screen with its capabilities and the option
to activate the official set (the agent, the chat…) with a single key, with
no network access. Without a TTY (CI, Docker, scripts), the one-command
equivalent is `enu --default-config`, which writes that activation to your
`enu.toml`. This is deliberate — see [Key
concepts](/enu/en/docs/conceptos/)—. For headless scripting with `enu -e`
you don't need to activate anything.
:::

## Next step

You can now run Lua. Continue with [Your first
script](/enu/en/docs/primer-script/).
