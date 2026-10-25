def _api(path, method="GET", form=None):
    u = config["base_url"] + path
    headers = {"Authorization": "Bearer " + config["secret_key"]}
    if form == None:
        r = net.request(method=method, url=u, headers=headers)
    else:
        r = net.post(url=u, body=url.query_encode(form), headers=headers,
                     content_type="application/x-www-form-urlencoded")
    if r["status"] < 200 or r["status"] >= 300:
        fail("stripe api: status " + str(r["status"]) + " for " + method + " " + path)
    return json.decode(r["body"])


def _money(d):
    return {"amount": str(decimal.from_minor(d["amount"], d["currency"])),
            "currency": d["currency"]}


def get_balance():
    d = _api("/v1/balance")
    return {
        "available": [_money(x) for x in d["available"]],
        "pending": [_money(x) for x in d["pending"]],
    }


def _charge(d):
    return {
        "id": d["id"], "amount": str(decimal.from_minor(d["amount"], d["currency"])),
        "currency": d["currency"], "status": d["status"], "paid": d["paid"],
        "description": d.get("description", ""),
    }


def list_charges(limit=10):
    d = _api("/v1/charges?limit=" + str(limit))
    return [_charge(x) for x in d["data"]]


def get_charge(charge_id):
    d = _charge(_api("/v1/charges/" + charge_id))
    return d


def list_customers(limit=10):
    d = _api("/v1/customers?limit=" + str(limit))
    return [{"id": c["id"], "email": c.get("email", ""), "name": c.get("name", ""),
             "created": c["created"]} for c in d["data"]]


def create_payment_link(price_id, quantity=1):
    d = _api("/v1/payment_links", form={
        "line_items[0][price]": price_id,
        "line_items[0][quantity]": str(quantity),
    })
    return {"id": d["id"], "url": d["url"]}
