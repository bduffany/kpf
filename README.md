# kpf

`kpf` is a drop-in replacement for `kubectl port-forward` that makes it
easier to manage port forwarding sessions.

## Features

### Background sessions

- `kpf` always starts connections in the background and just prints
  out a port number you can use.
- If a session is already running, it succeeds immediately and just
  returns the existing port.
- The session manager runs as a single daemon process that you can
  connect to from any terminal.
- Sessions are kept alive for a configurable TTL that defaults to 30
  minutes.

### Port search

- Type any name like `kpf prometheus` to search all ports for prometheus
  (services, pods, etc., across all namespaces).
- Search can be scoped within a namespace, e.g. `-n my_namespace` and then
  get a fuzzy search with the options matching that search.
- It uses `kubectl` to live query the available ports, and also
  keeps a history of ports you've forwarded most recently, to speed up
  the search if you've forwarded before.

### Aliases

- Assign aliases for quick forwarding, e.g. `kpf -a prometheus`
  - Then when you type `kpf prometheus` it will choose that option
- Aliases also show up first in the fuzzy-matching search options, in case
  you mistype them.

### Compatibility

`kpf` aims to be compatible with `kubectl port-forward`:

- Accepts all the same arguments
- Has the same `--help` output
- Supports tab completion

## Installation

### Option 1: Agent-driven installation (easiest)

Modern coding agents can reliably execute this install prompt:

```
1. Install github.com/bduffany/kpf@latest with go install
2. Install the agent skills from that repo.
3. Set up shell completion for kpf (see kpf repo README)
4. Install fzf, which is needed for kpf's search feature: github.com/junegunn/fzf
```

### Option 2: Manual installation

Install with `go`:

```bash
go install github.com/bduffany/kpf@latest
```

This installs `kpf` into `$GOBIN` (or `$(go env GOPATH)/bin` if `GOBIN` is unset).

Optionally install bash completion by adding this to your `.bashrc`
(docs for other shells [below](#shell-completion))

```bash
source <(kubectl completion bash)
complete -o default -F __start_kubectl kpf
```

## Basic usage

```bash
kpf --context=... --cluster=... --namespace=... pod/mypod 8080
# prints the chosen local port
```

```bash
kpf --namespace=default svc/my-service 9999:8080
# prints 9999
```

```bash
kpf --fg --namespace=default svc/my-service :8080
# runs kubectl port-forward in the foreground (no daemon)
```

## Aliases

```bash
kpf -a vmselect
# save or update alias "vmselect"; uses picker if args are underspecified
```

```bash
kpf -a vmselect -n monitor-prod svc/victoria-metrics-cluster-global-vmselect :8481
# save or update alias "vmselect" from explicit args
```

```bash
kpf
# bare picker shows history plus live discovered candidates; alias matches are shown as "<args> # <portname> (alias)"
```

## Shell completion

`kpf` supports `kubectl`'s completion protocol (`__complete` /
`__completeNoDesc`) and proxies those requests to `kubectl port-forward`.

### Bash

```bash
source <(kubectl completion bash)
complete -o default -F __start_kubectl kpf
```

### Zsh

```bash
source <(kubectl completion zsh)
compdef __start_kubectl kpf
```

## All options

<!-- BEGIN GENERATED: kpf-help -->

Supports all options from `kubectl port-forward`, plus:

```text
    --alias, -a NAME:
	Save or update NAME from resolved args and exit.

    --list:
	Print available port-forward targets discovered by kpf and exit.

    --fg, -f:
	Run kubectl port-forward in the foreground. Do not use the kpf daemon.

    --ttl=30m0s:
	Session TTL for daemon-managed forwards. Accepts Go duration values like 30m or 1h.
	Can also be set via KUBECTL_PORT_FORWARD_TTL (the --ttl flag takes higher priority).
```

<!-- END GENERATED: kpf-help -->

## Technical notes

- Uses a background daemon that keeps sessions alive for 30 minutes of
  inactivity by default (`KUBECTL_PORT_FORWARD_TTL` or `--ttl` args allow
  per-session overrides).
- Uses a daemon unix socket:
  - Linux: `/run/user/$UID/portfwd.sock`
  - macOS: `/tmp/kpf-$UID/portfwd.sock`
- Reuses existing forward sessions only when the request matches exactly
  after normalization.
  - A match includes all of:
    - Normalized `kubectl port-forward` args (same order, same values).
    - Resolved kube identity (`context`, `cluster`, `server`, `user`,
      `namespace`).
    - Session TTL.
  - Normalization rules:
    - Leading `port-forward` is removed, so `kpf port-forward ...` and
      `kpf ...` match.
    - `--ttl` is removed from args and stored as session TTL.
    - Port mappings are canonicalized:
      - `8080` and `:8080` both become `:8080`.
      - `09999:8080` becomes `9999:8080`.
  - Examples:
    - Reused: `kpf pod/api 8080` and `kpf pod/api :8080`
    - Not reused: `kpf -n team-a pod/api 8080` and
      `kpf -n team-b pod/api 8080`
    - Not reused: `kpf -n team-a pod/api 8080` and
      `kpf --namespace=team-a pod/api 8080` (different arg forms)
- Renews TTL when kubectl logs `Handling connection for ...`.
- If local/source port is omitted, chooses a random local port.
