import re

from .wire import APIError, instant, normalize, stamp, uuid

STATUSES = {"open", "accepted", "picked up", "delivered", "dispute", "completed", "cancelled", "expired", "not completed"}


def positive_decimal(value, bounded=False):
    if not isinstance(value, str) or not re.fullmatch(r"[1-9][0-9]*", value):
        raise ValueError("invalid amount")
    if bounded and (len(value) > 19 or int(value) > 9223372036854775807):
        raise ValueError("amount out of range")
    return value


def event_batch(body):
    if not isinstance(body, dict) or not isinstance(body.get("events"), list) or not 1 <= len(body["events"]) <= 100:
        raise APIError(400, "invalid_event")
    return [normalize_event(event) for event in body["events"]]


def normalize_event(event):
    try:
        if not isinstance(event, dict) or event["source"] != "order":
            raise ValueError("invalid producer")
        if type(event["schemaVersion"]) is not int or event["schemaVersion"] != 1:
            raise APIError(422, "unsupported_event")
        event_type = event["type"]
        fields = {
            "QuoteDisplayed": ("quoteId", "draft"),
            "OfferConfirmed": ("draft", "offeredCredits", "quoteId"),
            "PricingOrderCreated": ("orderId", "orderVersion", "createdAt", "expiresAt", "pickup", "delivery", "totalItemQuantity", "offeredCredits", "pricingQuoteId", "pricingDraft"),
            "PricingOrderOutcome": ("orderId", "orderVersion", "status", "offeredCredits", "pricingQuoteId"),
        }
        if event_type not in fields:
            raise APIError(422, "unsupported_event")
        data = {key: event["data"][key] for key in fields[event_type]}
        for key in ("quoteId", "orderId", "pricingQuoteId"):
            if key in data:
                nullable = key == "pricingQuoteId" or key == "quoteId" and event_type == "OfferConfirmed"
                if data[key] is not None or not nullable:
                    data[key] = uuid(data[key])
        for key in ("draft", "pricingDraft"):
            if key in data and (data[key] is not None or key == "draft"):
                data[key] = normalize("draft", data[key])
        for key in ("createdAt", "expiresAt"):
            if key in data:
                data[key] = stamp(instant(data[key]))
        if "orderVersion" in data and (type(data["orderVersion"]) is not int or not 1 <= data["orderVersion"] <= 9223372036854775807):
            raise ValueError("invalid version")
        if "offeredCredits" in data:
            positive_decimal(data["offeredCredits"], bounded=True)
        if "totalItemQuantity" in data:
            positive_decimal(data["totalItemQuantity"])
        if "status" in data and data["status"] not in STATUSES:
            raise APIError(422, "unsupported_event")
        for key in ("pickup", "delivery"):
            if key in data:
                place = normalize("place", data[key])
                location = data[key]["locationId"]
                if not isinstance(location, str) or not location:
                    raise ValueError("missing resolved location")
                data[key] = dict(place, locationId=location)
        return {"eventId": uuid(event["eventId"]), "source": "order", "type": event_type, "schemaVersion": 1, "occurredAt": stamp(instant(event["occurredAt"])), "data": data}
    except APIError:
        raise
    except Exception:
        raise APIError(400, "invalid_event") from None
