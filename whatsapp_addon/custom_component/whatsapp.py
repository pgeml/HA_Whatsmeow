import requests
from url_normalize import url_normalize


class WhatsappRequestError(Exception):
    def __init__(self, path, status_code, body_text):
        self.path = path
        self.status_code = status_code
        self.body_text = body_text
        super().__init__(f"POST {path} failed: status={status_code} body={body_text!r}")


class Whatsapp:
    def __init__(self, host="{{HOSTNAME}}", port=3000):
        self._port = port
        self._hosts = []
        for candidate in (host, "{{HOSTNAME}}", "localhost", "127.0.0.1"):
            candidate = (candidate or "").strip()
            if candidate and candidate not in self._hosts:
                self._hosts.append(candidate)

    def _post_ok(self, path, data):
        last_error = None
        for host in self._hosts:
            base = f"http://{host}:{self._port}"
            try:
                r = requests.post(url_normalize(f"{base}{path}"), json=data, timeout=60)
            except requests.RequestException as err:
                last_error = err
                continue

            if r.content == b"OK":
                return True
            raise WhatsappRequestError(path, r.status_code, r.text.strip())

        if last_error is not None:
            raise last_error
        raise WhatsappRequestError(path, 503, "no backend hosts available")

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
