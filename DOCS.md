# WhatsApp Add-on (whatsmeow / Multi-Device)

Drop-in replacement for the community `whatsapp_addon` that relied on Baileys/WhatsApp Web.
This add-on uses **whatsmeow** (WhatsApp Multi-Device protocol) for improved stability.

## What you get
- QR login via Add-on UI (Ingress)
- Persistent session stored in `/data`
- Home Assistant custom component auto-installed to `/config/custom_components/whatsapp` (same as the old add-on)
- Service compatibility for `whatsapp.send_message`
- Media sending for images, video, documents, and audio
- Home Assistant local media support via `/local/...` and `/media/...`

## Install
1. Add this repository to **Settings → Add-ons → Add-on Store → ⋮ → Repositories**
2. Install **WhatsApp**
3. Start the add-on, open it, scan the QR in WhatsApp → *Linked devices*
4. Use the `whatsapp.send_message` service in automations

## Configuration
- `clients`: list of client IDs (default: `["default"]`). Current implementation supports one active session; additional IDs are accepted for compatibility but mapped to the same session.

## Supported Services
- `whatsapp.send_message`
- `whatsapp.send_image`
- `whatsapp.send_video`
- `whatsapp.send_document`
- `whatsapp.send_audio`

## YAML Service Format
The existing Home Assistant YAML format is preserved:

```yaml
action: whatsapp.send_message
data:
  clientId: default
  to: 391234567890
  body:
    text: Hello from Home Assistant
```

Notes:
- `to` can be a full JID like `391234567890@s.whatsapp.net`
- `to` can also be a bare phone number like `391234567890`
- group JIDs like `120363171536423506@g.us` are supported

## Media Sources
Media services accept:
- direct `http://` and `https://` URLs that are publicly downloadable
- `/local/...` which maps to Home Assistant `/config/www/...`
- `/media/...`

Direct filesystem paths are also supported, but only under:
- `/config/www/...`
- `/media/...`

Remote URLs that require cookies, login, or anti-bot headers may fail even if they open in a browser.

## Media Examples
Image from Home Assistant local storage:

```yaml
action: whatsapp.send_image
data:
  clientId: default
  to: 120363171536423506@g.us
  body:
    url: /local/test.jpg
    caption: Test JPG from HA
    mimeType: image/jpeg
```

Document/PDF from `/media`:

```yaml
action: whatsapp.send_document
data:
  clientId: default
  to: 120363171536423506@g.us
  body:
    url: /media/test.pdf
    fileName: test.pdf
    caption: Test PDF from HA
    mimeType: application/pdf
```

Normal audio:

```yaml
action: whatsapp.send_audio
data:
  clientId: default
  to: 120363171536423506@g.us
  body:
    url: /media/sample.mp3
    ptt: false
    mimeType: audio/mpeg
```

Voice note:

```yaml
action: whatsapp.send_audio
data:
  clientId: default
  to: 120363171536423506@g.us
  body:
    url: /media/sample.ogg
    ptt: true
    mimeType: audio/ogg; codecs=opus
```

## Supported And Known Limits
Supported:
- text messages to individual and group JIDs
- JPEG and PNG images
- MP4 video
- PDF documents
- audio with `audio/mpeg`
- audio with `audio/mp4`
- voice notes with `audio/ogg; codecs=opus`
- Home Assistant local media through `/local/...` and `/media/...`

Known limits:
- generic `audio/ogg` is not specific enough for voice notes; use `audio/ogg; codecs=opus`
- remote media URLs must be directly downloadable and anonymously accessible
- only one active WhatsApp session is currently used even though `clients` remains in the config for compatibility

## Troubleshooting
- If WhatsApp logs out, open the add-on UI again to get a new QR.
- To reset the session, stop the add-on and delete `/data/store.db`, then start again.
- If local media paths fail, rebuild/restart the add-on so the latest backend is running.
- If remote media returns `403` or `404`, the remote host is rejecting or missing the file rather than the add-on failing to send.
