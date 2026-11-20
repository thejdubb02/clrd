# Stripe

`POST /{topic}?template=stripe` takes Stripe event webhooks. Each event is one
notification that links to it in the dashboard. Event types with `fail` or
`dispute` in them are priority high.

1. Developers, Webhooks, Add endpoint.
2. Endpoint URL: `https://clrd.example.com/billing?template=stripe&auth=<token>`
   ([how to make `<token>`](custom-webhooks.md#sending-the-token)).
3. Pick the events you want, for example `payment_intent.payment_failed` and
   `charge.dispute.created`.
4. Reveal the endpoint's signing secret (`whsec_...`) and save it in clrd so
   it checks `Stripe-Signature`:
   `curl -X PUT .../api/v1/topics/billing/templates/stripe -H "Authorization: Bearer clrd_..." -d '{"secret":"whsec_..."}'`
   Events signed more than 5 minutes ago are refused, as Stripe's own
   libraries do.

| clrd | Stripe |
|---|---|
| `title` | `type`, e.g. `payment_intent.payment_failed` |
| `message` | `data.object.object` and `data.object.id` |
| `click` | `https://dashboard.stripe.com/[test/]events/<id>` |
| `priority` | high when `type` contains `fail` or `dispute`, else 3 |

Source: [Stripe webhooks](https://docs.stripe.com/webhooks).
