import asyncio
import importlib
import logging
import sys
import types
import unittest
from unittest.mock import patch


def _install_homeassistant_stubs():
    if "requests" not in sys.modules:
        requests = types.ModuleType("requests")

        class RequestException(Exception):
            pass

        requests.RequestException = RequestException
        requests.post = lambda *args, **kwargs: None
        sys.modules["requests"] = requests

    if "url_normalize" not in sys.modules:
        url_normalize = types.ModuleType("url_normalize")
        url_normalize.url_normalize = lambda value: value
        sys.modules["url_normalize"] = url_normalize

    if "voluptuous" not in sys.modules:
        voluptuous = types.ModuleType("voluptuous")
        voluptuous.ALLOW_EXTRA = object()
        voluptuous.Optional = lambda key, default=None: key
        voluptuous.Schema = lambda *args, **kwargs: ("schema", args, kwargs)
        sys.modules["voluptuous"] = voluptuous

    if "homeassistant" not in sys.modules:
        homeassistant = types.ModuleType("homeassistant")
        homeassistant.__path__ = []
        sys.modules["homeassistant"] = homeassistant

    if "homeassistant.core" not in sys.modules:
        core = types.ModuleType("homeassistant.core")

        class HomeAssistant:
            pass

        class ServiceCall:
            def __init__(self, service, data):
                self.service = service
                self.data = data

        core.HomeAssistant = HomeAssistant
        core.ServiceCall = ServiceCall
        sys.modules["homeassistant.core"] = core

    if "homeassistant.helpers" not in sys.modules:
        helpers = types.ModuleType("homeassistant.helpers")
        helpers.__path__ = []
        sys.modules["homeassistant.helpers"] = helpers

    if "homeassistant.helpers.typing" not in sys.modules:
        typing_mod = types.ModuleType("homeassistant.helpers.typing")
        typing_mod.ConfigType = dict
        sys.modules["homeassistant.helpers.typing"] = typing_mod

    if "homeassistant.helpers.config_validation" not in sys.modules:
        cv = types.ModuleType("homeassistant.helpers.config_validation")
        cv.string = str
        cv.port = int
        sys.modules["homeassistant.helpers.config_validation"] = cv


class WhatsappLayerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        _install_homeassistant_stubs()

    def setUp(self):
        for module_name in (
            "whatsapp_addon.custom_component.whatsapp",
            "whatsapp_addon.custom_component.__init__",
        ):
            sys.modules.pop(module_name, None)

    def test_whatsapp_success_returns_true(self):
        from whatsapp_addon.custom_component.whatsapp import Whatsapp

        class Response:
            content = b"OK"
            status_code = 200
            text = "OK"

        calls = []

        def fake_post(url, json, timeout):
            calls.append((url, json, timeout))
            return Response()

        with patch("whatsapp_addon.custom_component.whatsapp.requests.post", new=fake_post):
            api = Whatsapp(host="127.0.0.1", port=3000)
            ok = api.send_message({"clientId": "default", "to": "123@s.whatsapp.net", "body": {"text": "hi"}})

        self.assertTrue(ok)
        self.assertEqual(
            calls,
            [
                (
                    "http://127.0.0.1:3000/sendMessage",
                    {"clientId": "default", "to": "123@s.whatsapp.net", "body": {"text": "hi"}},
                    60,
                )
            ],
        )

    def test_whatsapp_retries_other_hosts_after_connection_failure(self):
        from whatsapp_addon.custom_component.whatsapp import Whatsapp
        import requests

        class Response:
            content = b"OK"
            status_code = 200
            text = "OK"

        calls = []

        def fake_post(url, json, timeout):
            calls.append(url)
            if url.startswith("http://bad-host:3000"):
                raise requests.RequestException("connection failed")
            return Response()

        with patch("whatsapp_addon.custom_component.whatsapp.requests.post", new=fake_post):
            api = Whatsapp(host="bad-host", port=3000)
            ok = api.send_message({"clientId": "default", "to": "123@s.whatsapp.net", "body": {"text": "hi"}})

        self.assertTrue(ok)
        self.assertEqual(
            calls,
            [
                "http://bad-host:3000/sendMessage",
                "http://{{HOSTNAME}}:3000/sendMessage",
            ],
        )

    def test_whatsapp_failure_preserves_endpoint_status_and_body(self):
        from whatsapp_addon.custom_component.whatsapp import Whatsapp, WhatsappRequestError

        class Response:
            content = b"KO: upstream returned 404"
            status_code = 502
            text = "KO: upstream returned 404\n"

        with patch(
            "whatsapp_addon.custom_component.whatsapp.requests.post",
            new=lambda url, json, timeout: Response(),
        ):
            api = Whatsapp()

            with self.assertRaises(WhatsappRequestError) as ctx:
                api.send_document(
                    {"clientId": "default", "to": "123@s.whatsapp.net", "body": {"url": "https://x/y.pdf"}}
                )

        err = ctx.exception
        self.assertEqual(err.path, "/sendDocument")
        self.assertEqual(err.status_code, 502)
        self.assertEqual(err.body_text, "KO: upstream returned 404")

    def test_payload_keeps_existing_body_text_shape(self):
        module = importlib.import_module("whatsapp_addon.custom_component.__init__")

        call = sys.modules["homeassistant.core"].ServiceCall(
            "send_message",
            {"clientId": "default", "to": "123@s.whatsapp.net", "body": {"text": "hello"}},
        )

        payload = module._payload(call)

        self.assertEqual(
            payload,
            {"clientId": "default", "to": "123@s.whatsapp.net", "body": {"text": "hello"}},
        )

    def test_payload_supports_top_level_text_for_backward_compatibility(self):
        module = importlib.import_module("whatsapp_addon.custom_component.__init__")

        call = sys.modules["homeassistant.core"].ServiceCall(
            "send_message",
            {"clientId": "default", "to": "123@s.whatsapp.net", "text": "hello"},
        )

        payload = module._payload(call)

        self.assertEqual(
            payload,
            {"clientId": "default", "to": "123@s.whatsapp.net", "body": {"text": "hello"}},
        )

    def test_async_setup_registers_async_handlers_and_uses_send_message(self):
        module = importlib.import_module("whatsapp_addon.custom_component.__init__")

        class FakeWhatsapp:
            def __init__(self, host="{{HOSTNAME}}", port=3000):
                self.host = host
                self.port = port

            def send_message(self, data):
                return True

        class FakeServices:
            def __init__(self):
                self.registered = {}

            def async_register(self, domain, service, callback):
                self.registered[(domain, service)] = callback

        class FakeHass:
            def __init__(self):
                self.services = FakeServices()
                self.executor_calls = []

            async def async_add_executor_job(self, func, payload):
                self.executor_calls.append((func.__name__, payload))
                return func(payload)

        class FakeCall:
            def __init__(self, service, data):
                self.service = service
                self.data = data

        with patch.object(module, "Whatsapp", FakeWhatsapp):
            hass = FakeHass()
            asyncio.run(module.async_setup(hass, {}))

            callback = hass.services.registered[(module.DOMAIN, "send_message")]
            self.assertTrue(asyncio.iscoroutinefunction(callback))

            call = FakeCall("send_message", {"to": "123@s.whatsapp.net", "text": "hello"})
            asyncio.run(callback(call))

        self.assertEqual(
            hass.executor_calls,
            [
                (
                    "send_message",
                    {"clientId": "default", "to": "123@s.whatsapp.net", "body": {"text": "hello"}},
                )
            ],
        )

    def test_homeassistant_logs_service_failure_details(self):
        module = importlib.import_module("whatsapp_addon.custom_component.__init__")

        class FakeWhatsapp:
            def __init__(self, host="{{HOSTNAME}}", port=3000):
                pass

            def send_audio(self, data):
                raise module.WhatsappRequestError("/sendAudio", 502, "KO: fetch failed")

        class FakeServices:
            def __init__(self):
                self.registered = {}

            def async_register(self, domain, service, callback):
                self.registered[(domain, service)] = callback

        class FakeHass:
            def __init__(self):
                self.services = FakeServices()

            async def async_add_executor_job(self, func, payload):
                return func(payload)

        class FakeCall:
            def __init__(self, service, data):
                self.service = service
                self.data = data

        with patch.object(module, "Whatsapp", FakeWhatsapp):
            hass = FakeHass()
            asyncio.run(module.async_setup(hass, {}))

            callback = hass.services.registered[(module.DOMAIN, "send_audio")]
            call = FakeCall(
                "send_audio",
                {"clientId": "default", "to": "123@s.whatsapp.net", "body": {"url": "https://example.com/a.ogg"}},
            )

            with self.assertLogs(module._LOGGER, level=logging.ERROR) as captured:
                asyncio.run(callback(call))

        joined = "\n".join(captured.output)
        self.assertIn("WhatsApp service send_audio failed", joined)
        self.assertIn("endpoint=/sendAudio", joined)
        self.assertIn("status=502", joined)
        self.assertIn("KO: fetch failed", joined)


if __name__ == "__main__":
    unittest.main()
