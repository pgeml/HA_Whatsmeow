import logging
import voluptuous as vol

from homeassistant.core import HomeAssistant, ServiceCall
from homeassistant.helpers.typing import ConfigType
from homeassistant.helpers import config_validation as cv

from .whatsapp import Whatsapp, WhatsappRequestError

_LOGGER = logging.getLogger(__name__)

DOMAIN = "whatsapp"

CONF_HOST = "host"
CONF_PORT = "port"

CONFIG_SCHEMA = vol.Schema(
    {
        DOMAIN: vol.Schema(
            {
                vol.Optional(CONF_HOST, default="127.0.0.1"): cv.string,
                vol.Optional(CONF_PORT, default=3000): cv.port,
            }
        )
    },
    extra=vol.ALLOW_EXTRA,
)

def _payload(call: ServiceCall) -> dict:
    # Map HA service data -> backend payload keys
    data = dict(call.data)
    # Normalize keys (keep your existing naming)
    payload = {
        "clientId": data.get("clientId", "default"),
        "to": data.get("to", ""),
        "body": data.get("body", {}),
    }
    # Some services use different key
    if call.service == "set_status":
        payload = {"clientId": data.get("clientId", "default"), "status": data.get("status", "")}
    if call.service == "presence_subscribe":
        payload = {"clientId": data.get("clientId", "default"), "userId": data.get("userId", "")}
    if call.service in ("send_presence_update", "send_infinity_presence_update"):
        payload = {
            "clientId": data.get("clientId", "default"),
            "type": data.get("type", "available"),
            "to": data.get("to", ""),
        }
    return payload

async def async_setup(hass: HomeAssistant, config: ConfigType) -> bool:
    host = config.get(DOMAIN, {}).get(CONF_HOST, "127.0.0.1")
    port = config.get(DOMAIN, {}).get(CONF_PORT, 3000)

    api = Whatsapp(host=host, port=port)

    async def _call(service_name: str, func_name: str, call: ServiceCall) -> None:
        payload = _payload(call)
        try:
            ok = await hass.async_add_executor_job(getattr(api, func_name), payload)
            if not ok:
                _LOGGER.error("WhatsApp service %s failed (payload keys=%s)", service_name, list(payload.keys()))
        except WhatsappRequestError as e:
            _LOGGER.error(
                "WhatsApp service %s failed: endpoint=%s status=%s body=%r",
                service_name,
                e.path,
                e.status_code,
                e.body_text,
            )
        except Exception as e:
            _LOGGER.exception("WhatsApp service %s exception: %s", service_name, e)

    # Text
    hass.services.async_register(DOMAIN, "send_message", lambda call: _call("send_message", "send_message", call))

    # Media
    hass.services.async_register(DOMAIN, "send_image", lambda call: _call("send_image", "send_image", call))
    hass.services.async_register(DOMAIN, "send_video", lambda call: _call("send_video", "send_video", call))
    hass.services.async_register(DOMAIN, "send_document", lambda call: _call("send_document", "send_document", call))
    hass.services.async_register(DOMAIN, "send_audio", lambda call: _call("send_audio", "send_audio", call))

    return True
