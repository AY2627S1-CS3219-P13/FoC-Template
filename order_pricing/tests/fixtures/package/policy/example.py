"""Deterministic test double; constant price deliberately has no pricing validity."""

from pricing_contracts import PolicyInput, PolicyResult, UnsupportedRoute


class ExamplePolicy:
    def warm_up(self, example: PolicyInput) -> None:
        self.calculate(example)

    def calculate(self, value: PolicyInput) -> PolicyResult:
        places = value.resolved_places
        if not any(
            route["pickupLocationId"] == places["pickupLocationId"]
            and route["deliveryLocationId"] == places["deliveryLocationId"]
            for route in value.context["routes"]
        ):
            raise UnsupportedRoute()
        warnings = []
        if "totalItemQuantity" not in value.request:
            warnings.append("unknown_item_quantity")
        state = value.context["orderContext"]["state"]
        if state != "fresh":
            warnings.append(
                "stale_order_context" if state == "stale" else "order_context_unavailable"
            )
        return PolicyResult(8, "baseline", tuple(warnings), "test-only-1", {})

    def close(self) -> None:
        pass


def load_policy(package_dir):
    return ExamplePolicy()
