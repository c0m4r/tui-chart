# Development helper for sandboxed sessions.
#
# The Go toolchain normally keeps its module and build caches in $HOME. When
# that location is not writable (containers, sandboxes), source this file to
# move the caches into the repository and to use the shared module download
# cache as a read only proxy:
#
#   . ./scripts/env.sh && go test ./...
#
# It is not needed for a normal checkout.

export GOMODCACHE="${GOMODCACHE:-$PWD/.gocache/mod}"
export GOCACHE="${GOCACHE:-$PWD/.gocache/build}"
export GOFLAGS="${GOFLAGS:--mod=mod}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"
if [ -d "$HOME/go/pkg/mod/cache/download" ]; then
  export GOPROXY="file://$HOME/go/pkg/mod/cache/download,https://proxy.golang.org,direct"
  export GOSUMDB=off
fi
