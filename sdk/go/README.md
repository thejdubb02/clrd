# clrd for Go

Stdlib only.

```sh
go get github.com/thejdubb02/clrd/sdk/go
```

```go
import clrd "github.com/thejdubb02/clrd/sdk/go"

c := clrd.New("https://clrd.example.com", "clrd_...") // or New("", "") for CLRD_URL / CLRD_TOKEN
c.Send(ctx, clrd.Message{Topic: "infra", Message: "Disk 91%", ID: "disk-build", Priority: 4})
c.Resolve(ctx, "infra", "disk-build") // clears it on every device

n, _ := c.Send(ctx, clrd.Message{Topic: "deploys", Message: "Ship v2?"})
c.AckID(ctx, n.ID)
c.Snooze(ctx, n.ID, "1h")
c.Mark(ctx, n.ID, "actioned", "filed #5", "") // others see it; it clears nothing
c.List(ctx, "infra", "open")
```

Non-2xx answers return `*clrd.Error` with `Status` and `Code`.

Tests: `go test ./...`

Send options are fields on `clrd.Message`. Use `Icon` for an image URL,
`Attachment: &clrd.Attachment{URL: "..."}` for a file link, and
`Actions: []clrd.Action{{Label: "Copy", Copy: "build-42"}}` for a copy button.
Reuse `ID` on the same topic to update an open notification. Set `Reopen` to
make an acknowledged notification active when updating it. Delivery options
include `SnoozeUntil`, `Expires`, `Repeat`, `RepeatTimes`, `Live`, and `Devices`.

Source: [github.com/thejdubb02/clrd](https://github.com/thejdubb02/clrd)
