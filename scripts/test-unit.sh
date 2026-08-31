#!/bin/sh
set -eu

unformatted="$(/usr/local/go/bin/gofmt -l .)"
if [ -n "$unformatted" ]; then
    printf 'Unformatted files:\n%s\n' "$unformatted"
    exit 1
fi

/usr/local/go/bin/go vet ./...
/usr/local/go/bin/go test -count=1 ./...
