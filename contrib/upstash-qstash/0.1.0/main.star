def _auth(extra=None):
    h = {"Authorization": "Bearer " + config["token"]}
    if extra != None:
        for k in extra:
            h[k] = extra[k]
    return h


def _check(r, what):
    if r["status"] < 200 or r["status"] >= 300:
        fail("qstash: " + what + " status " + str(r["status"]))
    return json.decode(r["body"])


def publish(destination, body, delay_seconds=0, headers=None):
    h = _auth(headers)
    if delay_seconds > 0:
        h["Upstash-Delay"] = str(delay_seconds) + "s"
    if type(body) != "string":
        body = json.encode(body)
        h["Content-Type"] = "application/json"
    d = _check(net.post(url=config["base_url"] + "/v2/publish/" + destination,
                        body=body, headers=h), "publish")
    return {"message_id": d["messageId"]}


def create_schedule(destination, cron, body="", headers=None):
    h = _auth(headers)
    h["Upstash-Cron"] = cron
    d = _check(net.post(url=config["base_url"] + "/v2/schedules/" + destination,
                        body=body, headers=h), "create_schedule")
    return {"schedule_id": d["scheduleId"]}


def list_schedules():
    ds = _check(net.get(url=config["base_url"] + "/v2/schedules",
                        headers=_auth()), "list_schedules")
    return [{"schedule_id": s["scheduleId"], "cron": s["cron"],
             "destination": s["destination"]} for s in ds]
