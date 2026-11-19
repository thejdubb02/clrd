# sdk/go/README.md: the usage example, as written, inside a main function.
{
  echo 'package main'
  echo 'import ("context"; clrd "github.com/thejdubb02/clrd/sdk/go")'
  echo 'func main() {'
  echo 'ctx := context.Background()'
  snippet ../../sdk/go/README.md 'c.Resolve(' | grep -v '^import '
  echo '_ = n'
  echo '}'
} >"$WORK/main.go"
printf 'module example\ngo 1.21\nrequire github.com/thejdubb02/clrd/sdk/go v0.0.0\nreplace github.com/thejdubb02/clrd/sdk/go => /sdk\n' >"$WORK/go.mod"
dock -e GOTOOLCHAIN=local -e GOFLAGS=-buildvcs=false -v "$ROOT/sdk/go:/sdk:ro" -v "$WORK:/w:ro" golang:1.23 sh -ec '
  mkdir /tmp/ex && cp /w/main.go /w/go.mod /tmp/ex && cd /tmp/ex && go run .'
expect infra disk-build '.state == "resolved" and .priority == 4' "send then resolve by key"
expect deploys "" '.state == "snoozed" and .message == "Ship v2?"' "ack then snooze by id"
