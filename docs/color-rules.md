# Color rules

Color rules color your cards, so a glance at the inbox says where something
came from and how bad it is: Gatus alerts in red, anything tagged backup in
blue. Rules live on the server, so every web inbox you sign in to shows the
same colors. The Android app does not show them yet. With no rules, cards
look as they always do.

## How a rule matches

A rule can match on any of these. An empty field matches anything.

- **Topic**: the topic the card landed in.
- **Tag**: one of its tags, exact.
- **Priority**: a range from 1 (min) to 5 (urgent), both ends included.
- **Title text**: text found anywhere in the title, in any case.

The first rule whose set fields all match a card wins, so order matters: put
the narrow rules first. A rule also beats a color the sender asked for with
`"color"` on publish; with no matching rule, a sender's color shows as a bar.

## Styles

- **Bar**: a stripe of the color on the card's edge.
- **Tint**: a light wash of the color behind the card.
- **Solid**: the whole card in the color, with text picked to stay readable.
- **Gradient**: the color fading across the card.

Color is never the only signal: the priority and state chips stay on every
card. Green means cleared in clrd, so Settings notes when a rule uses a green
close to the clear color.

## In Settings

Open Settings, then Display, then Color rules. Add a rule, fill in what it
matches, pick a color and a style, and move it up or down. Nothing changes
until you press Save rules. Two presets fill the list for you to save:

- **Severity**: priority 5 red (`#DC2626`), 4 orange (`#EA580C`), 3 yellow
  (`#CA8A04`), all bars.
- **By topic**: one bar per topic, each its own color.

A preset replaces the list in the form; reload the page to drop it unsaved.

## Over the API and MCP

Rules are the `color_rules` setting. A `PUT` replaces the whole list, so read
it first and send every rule to keep; `[]` clears it.

```sh
curl -X PUT -H "Authorization: Bearer $CLRD_TOKEN" -H "Content-Type: application/json" \
  -d '{"color_rules": [
    {"topic": "gatus", "priority_min": 4, "color": "#DC2626", "style": "tint"},
    {"tag": "backup", "color": "#2563EB", "style": "bar"}
  ]}' \
  https://clrd.example.com/api/v1/settings
```

Each rule has `topic`, `tag`, `priority_min` and `priority_max` (1 to 5, 0
for no limit), `title`, `color` (`#RRGGBB`) and `style` (`bar`, `tint`,
`solid` or `gradient`). Up to 50 rules. A bad rule is a 400 that names its
index and field, and nothing changes.

Agents use the MCP `settings` tool with the same `color_rules` field, so one
call can set up a whole scheme, for example "color topic gatus by priority,
bar style".

## Who can change them

Any token with no topic list and no per-topic grants. A token limited to some
topics reads only the rules with no topic or a topic it may use, cannot
change them, and does not see Color rules in Settings.

Not built yet: matching by sender.
