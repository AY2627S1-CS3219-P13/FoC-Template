# TDD evidence

Implementation began only after the corresponding red baseline was observed in containers.

1. `go test ./internal/schedule -run 'TestWindow|TestHours|TestSlots'`: failed because `ValidateWindow`, `HoursCover` and `AvailableSlots` did not exist. After implementation, the pure window/hours suite passed with `-race`.
2. Schedule persistence/HTTP tests: failed because `App`, `Migrate`, `Config` and `New` did not exist. After implementation and correcting the fake to match Credit's documented release response, creation/idempotency, cancellation/expiry and lost-transfer-response/restart tests passed with `-race`.
3. Order `TestSchedule*`: failed because the scheduled-import configuration/contract did not exist. After implementation, concurrent combined-capacity, authenticated idempotent import and delivery-window tests passed.
4. `TestScheduleChildPage`: failed (`/orders` lacked Schedule order navigation). Page/navigation implementation followed.
5. Node `scheduled creation preserves int64 values and replaces manual expiry`: failed with “Choose an expiry in Singapore time.” The serializer was extended to use windowStart directly and preserve BigInt values; all five serializer tests then passed.

The initial service red baselines were compile failures for absent APIs, not behavioral assertion failures. The child-page and serialization baselines were observable assertion/runtime failures. Full regression and live results are recorded below when completed; no unexecuted check is claimed as passing.
6. The Sunday-only range/parser regression failed before strict weekday/digit fixes, then passed in the complete Schedule suite.
7. Production configuration check failed because Schedule lacked image-only deployment/bounded logs. Deployment sequencing test failed before Schedule was added to image pull/stop/readiness verification. Production configuration now passes; script regression results are recorded after a resource-isolated rerun.
8. `TestMalformedTransferReplyKeepsOwnershipUnresolved` failed with “malformed reply finalized ownership.” The worker now bounds the response and requires exactly one complete JSON value before finalizing transfer, retaining pending ownership on malformed replies.
9. `TestCreationReplayAfterTransferUsesAuthoritativeOrder` failed because the retained Schedule record reported accepted with no courier after ownership transfer. Saved creation retries now use 303 to the protected Order detail route, preserving its current lifecycle state and assignment rather than returning stale history.

## Verification completed

- Schedule: gofmt, vet, race tests and build passed; includes reservation/save failure, concurrent acceptance, lost/malformed handoff replies, restart recovery, slot/hour boundaries and immutable retries.
- Order: gofmt, vet, full race suite (151.689 seconds) and build passed with the three-slot fixtures retaining their original rollback/isolation assertions.
- Browser: all 19 Playwright cases and all five serializer tests passed on the final frontend revision (1.5 minutes).
- Gateway smoke passed and removed its generated admin/supplier/wallet fixtures.
- Real User/Supplier/Order/Credit/Schedule lifecycle passed: reserve exactly once, asynchronous import, same escrow, authoritative cancellation and exactly one refund. The rerun also passed the post-transfer creation-replay assertion, returning the current cancelled state and assigned courier from Order.
- Nine deployment sequencing/failure tests and image-only/bounded-logging production configuration check passed.

An overloaded parallel runtime build caused timing failures and an overall timeout in an intermediate Order run, plus a browser-fixture startup failure. That build was cancelled and images built serially; existing timing assertions were preserved. The isolated full Order run and final browser rerun above passed. AWS resources were not provisioned or deployed.

Final failed-save retry review: the extended test failed because unresolved cleanup returned 409 creation_abandoned. It now returns 503 creation_cleanup_pending until Credit's abort fence is confirmed, preserving the same-key retry flow; confirmed abandonment still returns 409 afterward. The complete Schedule check suite then passed (race tests: 1.910 seconds), and the final runtime readiness check returned ready.

## Embedded event migration (7 November 2026)

Scheduling now uses a durable journal in Order's database. Added regression coverage for atomic event/pending-state writes, effect/receipt rollback, concurrent replay and legacy copy/refusal to overwrite. Existing scheduling failure/financial tests explicitly drive both consumers and recovery. Original HTTP fake responses remain test-only fault injection; production scheduling has no Order HTTP client. Container validation results are reported in the implementation chat.
