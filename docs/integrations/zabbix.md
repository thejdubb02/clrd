# Zabbix

`?template=zabbix` takes the body this media type script sends. A problem
opens a notification and its recovery resolves it. Severity Disaster is
urgent, High is high, Not classified and Information are low.

1. Alerts, Media types, Create media type, type Webhook, name `clrd`.
2. Parameters:

   | Name | Value |
   |---|---|
   | `url` | `https://clrd.itsjustin.me/zabbix?template=zabbix` |
   | `token` | your clrd token |
   | `subject` | `{ALERT.SUBJECT}` |
   | `message` | `{ALERT.MESSAGE}` |
   | `severity` | `{EVENT.NSEVERITY}` |
   | `value` | `{EVENT.VALUE}` |
   | `eventid` | `{EVENT.ID}` |

3. Script:

   ```js
   var p = JSON.parse(value);
   var req = new HttpRequest();
   req.addHeader('Content-Type: application/json');
   req.addHeader('Authorization: Bearer ' + p.token);
   var resp = req.post(p.url, JSON.stringify({
     subject: p.subject, message: p.message, severity: p.severity,
     value: p.value, eventid: p.eventid
   }));
   if (req.getStatus() != 200) { throw 'clrd: ' + req.getStatus() + ' ' + resp; }
   return 'OK';
   ```

4. Give a user this media, and add it to a trigger action's problem and
   recovery operations.

| clrd | Zabbix |
|---|---|
| `title` | `{ALERT.SUBJECT}` |
| `message` | `{ALERT.MESSAGE}` |
| `external_id` | `zabbix-{EVENT.ID}` |
| resolve | `{EVENT.VALUE}` is 0 |
| `priority` | from `{EVENT.NSEVERITY}` 0 to 5 |

Sources: [webhook media type](https://www.zabbix.com/documentation/current/en/manual/config/notifications/media/webhook),
[macros](https://www.zabbix.com/documentation/current/en/manual/appendix/macros/supported_by_location).
