# clrd for Go

Stdlib only.

```sh
go get github.com/thejdubb02/clrd/sdk/go
```

```go
import clrd "github.com/thejdubb02/clrd/sdk/go"

c := clrd.New("https://clrd.itsjustin.me", "clrd_...") // or New("", "") for CLRD_URL / CLRD_TOKEN
c.Send(ctx, clrd.Message{Topic: "infra", Message: "Disk 91%", ID: "disk-build", Priority: 4})
c.Resolve(ctx, "infra", "disk-build") // clears it on every device

n, _ := c.Send(ctx, clrd.Message{Topic: "deploys", Message: "Ship v2?"})
c.AckID(ctx, n.ID)
c.Snooze(ctx, n.ID, "1h")
c.List(ctx, "infra", "open")
```

Non-2xx answers return `*clrd.Error` with `Status` and `Code`.

Tests: `go test ./...`
