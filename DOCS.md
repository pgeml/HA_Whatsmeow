# WhatsApp Add-on (whatsmeow / Multi-Device)

Drop-in replacement for the community `whatsapp_addon` that relied on Baileys/WhatsApp Web.
This add-on uses **whatsmeow** (WhatsApp Multi-Device protocol) for improved stability.

## What you get
- QR login via Add-on UI (Ingress)
- Persistent session stored in `/data`
- Home Assistant custom component auto-installed to `/config/custom_components/whatsapp` (same as the old add-on)
- Service compatibility: `whatsapp.send_message` (and presence/status helpers)

## Install
1. Add this repository to **Settings → Add-ons → Add-on Store → ⋮ → Repositories**
2. Install **WhatsApp**
3. Start the add-on, open it, scan the QR in WhatsApp → *Linked devices*
4. Use the `whatsapp.send_message` service in automations

## Configuration
- `clients`: list of client IDs (default: `["default"]`). Current implementation supports one active session; additional IDs are accepted for compatibility but mapped to the same session.

## Sending a message (service)
`whatsapp.send_message`
- `clientId`: `default`
- `to`: `391234567890@s.whatsapp.net` (or `+391234567890`)
- `body`: text

## Troubleshooting
- If WhatsApp logs out, open the add-on UI again to get a new QR.
- To reset the session, stop the add-on and delete `/data/store.db`, then start again.

