def _addrs(a):
    if type(a) == "string":
        a = [a]
    return [{"email": x} for x in a]


def send_email(sender, to, subject, text="", html="", cc=None):
    content = []
    if text != "":
        content.append({"type": "text/plain", "value": text})
    if html != "":
        content.append({"type": "text/html", "value": html})
    if not content:
        fail("sendgrid: text or html is required")
    p = {
        "personalizations": [{"to": _addrs(to)}],
        "from": {"email": sender},
        "subject": subject,
        "content": content,
    }
    if cc != None:
        p["personalizations"][0]["cc"] = _addrs(cc)
    r = net.post(
        url=config["base_url"] + "/v3/mail/send",
        body=json.encode(p),
        headers={"Authorization": "Bearer " + config["api_key"]},
        content_type="application/json",
    )
    if r["status"] < 200 or r["status"] >= 300:
        fail("sendgrid api: status " + str(r["status"]))
    return True
