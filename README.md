# runin

Run a command across multiple directories at once. Useful for managing several repos, services, or project folders without writing a shell loop every time.

# Contributing

Contributions are welcome, just do not use AI.

## Install

```
go install github.com/emy/runin@latest
```

Or clone and build:

```
git clone https://github.com/emy/runin.git
cd runin
go build -o runin .
```

## How it works

You give it a list of directories (or globs, or named groups), a `--` separator, and then the command you want to run. It enters each directory and runs that command for you.

```
runin <targets...> -- <command>
```

## Examples

Pull all your repos:
```
runin ~/projects/* -- git pull
```

Run tests in parallel across your services:
```
runin -parallel -j4 services/* -- make test
```

Only touch repos that have uncommitted work:
```
runin -dirty ~/code/* -- git stash
```

Use a shell pipeline:
```
runin -shell apps/* -- 'npm install && npm test'
```

Use a named group from your config:
```
runin group:work -- git fetch --prune
```

## Config

You can define directory groups in a `.runin.json` or `runin.json` file. The tool looks for it in the current directory first, then your home directory.

```json
{
    "groups": {
        "work": [
            "~/projects/work/*",
            "~/projects/clients/*"
        ],
        "personal": [
            "~/sideprojects/*",
            "~/dotfiles"
        ],
        "all": [
            "group:work",
            "group:personal"
        ]
    }
}
```

Groups can reference other groups. Paths support `~` expansion and environment variables like `$HOME` or `$PROJECT_ROOT`.

Comments (`//`) on their own line are allowed in the config file.

## Flags

| Flag | Description |
|------|-------------|
| `-parallel` | Run commands concurrently instead of one at a time |
| `-j N` | Limit parallel workers (defaults to number of CPU cores) |
| `-shell` | Run through `sh -c` (or `cmd /c` on Windows) |
| `-dirty` | Only process git repos that have local changes |
| `-q` | Quiet mode, only shows command output |
| `-dry` | Show what would run without running it |
| `-config path` | Use a specific config file |

## Platform support

Works on Linux, macOS, and Windows. Shell wrapping adapts automatically (`sh -c` vs `cmd /c`). Paths with `~` and environment variables are expanded on all platforms.

## License

MIT