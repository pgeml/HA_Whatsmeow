import requests
from url_normalize import url_normalize

class WhatsappRequestError(Exception):
    def __init__(self, path, status_code, body_text):
        self.path = path
        self.status_code = status_code
        self.body_text = body_text
        super().__init__(f"POST {path} failed: status={status_code} body={body_text!r}")

class Whatsapp:
    def __init__(self, host="127.0.0.1", port=3000):
        self._base = f"http://{host}:{port}"

    def _post_ok(self, path, data):
        r = requests.post(url_normalize(f"{self._base}{path}"), json=data, timeout=60)
        if r.content == b"OK":
            return True
        raise WhatsappRequestError(path, r.status_code, r.text.strip())

    def send_message(self, data):
        return self._post_ok("/sendMessage", data)

    def set_status(self, data):
        return self._post_ok("/setStatus", data)

    def presence_subscribe(self, data):
        return self._post_ok("/presenceSubscribe", data)

    def send_presence_update(self, data):
        return self._post_ok("/sendPresenceUpdate", data)

    def send_infinity_presence_update(self, data):
        return self._post_ok("/sendInfinityPresenceUpdate", data)

    # Media
    def send_image(self, data):
        return self._post_ok("/sendImage", data)

    def send_video(self, data):
        return self._post_ok("/sendVideo", data)

    def send_document(self, data):
        return self._post_ok("/sendDocument", data)

    def send_audio(self, data):
        return self._post_ok("/sendAudio", data)
