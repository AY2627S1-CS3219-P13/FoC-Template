# Friend on Campus project brief

CS3219 AY2627 Semester 1 · Group 13 · Updated 18 September 2026

This document records the project backlog, technology direction and deployment strategy. The backlog below is a paraphrased summary of the supplied D1 document. Sprint labels are its planned iterations, not claims of completed work. Unresolved technical choices remain explicitly open.

User Service implementation is now authorised, following the recommended Go/PostgreSQL/session design. Its [README](../user-service/README.md) records implemented behaviour and its [API guide](../user-service/API.md) describes the contract for teammates. **Credit integration is explicitly deferred**, including wallet creation, initial credits and verification events. These User Service choices do not finalise technology choices for every other service.

## Product and scope

Friend on Campus lets students request campus errands and fulfil requests for others. A student may participate as both requester and courier. Credits reward the courier's work and circulate within a closed economy; they cannot be purchased, withdrawn, exchanged for money, gifted or manually transferred.

The project requires four microservices in one repository, with one top-level folder per service:

| Service | Responsibility | Ownership |
| --- | --- | --- |
| User | Registration, authentication, profiles and user access | Keith |
| Supplier | Campus suppliers, facilities and pickup locations | To confirm |
| Order | Requests, courier assignment and errand lifecycle | To confirm |
| Credit | Balances, reservations, transfers and transaction history | To confirm |

A responsive frontend, meaningful asynchronous/event-driven behaviour, and containerised application and supporting components are required. The supplier dataset must include the provided seed data and additional listings. Each team member must contribute at least one nice-to-have feature worth of effort.

Payment for purchased items, supplier menus/catalogues/inventory/checkout and live courier tracking are outside the required scope. Credits do not pay suppliers.

## Technology choices

| Area | Current direction | Status |
| --- | --- | --- |
| Frontend | TypeScript and Next.js | Current team stack direction |
| Backend | Go for the services | Current team stack direction; framework and versions open |
| Application hosting | AWS EC2 | Confirmed team decision |
| Account access | Own email/password registration and school-email verification | Current product direction; external provider login not requested |
| Authentication mechanism | PostgreSQL-backed sessions; internal session validation endpoint | Adopted for User Service |
| Database | PostgreSQL for User Service | Adopted for User Service; other services open |
| Database hosting | Database on EC2 versus managed RDS | Not finalised |
| Messaging | RabbitMQ discussed as a candidate | Not finalised |
| Event reliability | Outbox discussed as an option | **Not finalised; not a requirement to implement** |
| Container tooling | Docker and Docker Compose | Local User Service setup implemented; full-team deployment open |
| Email delivery | Local Mailpit capture; SMTP adapter with STARTTLS for deployment | Production provider not selected; SES remains an option |

The frontend provides the user experience; the four backend services cover the required business areas. User Service's current design is documented in its service folder. Other previously discussed designs remain proposals unless explicitly adopted.

## Functional backlog

### User Service — FR1

**S1:** register with school email, display name and password; reject display names already assigned to an active account; require a password of at least 12 characters. Verify email ownership using a one-time code, activating the account only when the correct code is submitted within 30 minutes. Permit verified users to log in; reject invalid credentials without revealing sensitive information. Identify the authenticated user and assigned roles. Logout must invalidate the active session or token.

**S2:** allow profile changes such as display name and password; support authenticated administrative role assignment and revocation.

### Supplier Service — FR2

**S1:** maintain supplier records with a unique identifier, name, category, location and active/inactive status. Description and opening hours are optional; new suppliers start active. Categories are food, printing, retail, services and other. Locations must come from a controlled campus list. Reject duplicate active supplier names at the same location.

Authenticated users can browse active suppliers and view their details. Only admins can create, update or deactivate suppliers. Deactivation retains the record but removes it from new pickup options. Load the supplied seed dataset without creating duplicates on repeated loading, and expand the listing as required by the brief.

**S2:** category filtering and creation timestamps, following the backlog's current allocation.

### Order Service — FR3

**S1:** create an errand with exactly one active pickup supplier or pickup location, one delivery location, offered credits and one to five item entries. Each item needs a name/description and quantity; the load must be reasonable for one courier. Creation requires sufficient available credits.

Couriers can browse open requests with pickup/delivery information, items, reward and expiry. An open, unexpired request can have only one assigned courier, including under simultaneous acceptance. Requesters cannot accept their own orders; accepted requests leave the open list.

The assigned courier updates accepted, picked-up and delivered progress. The requester confirms successful delivery only after it is marked delivered. Support eligible cancellations and expiry of unaccepted requests, preventing acceptance after expiry.

**S2:** penalties for requester cancellation after pickup. Eligibility and penalty rules still require agreement.

### Credit Service — FR4

**S1:** maintain a wallet and auditable transaction history for every account. Award 100 initial credits exactly once after successful account verification. Display available and reserved balances separately, with history of allocations, reservations, releases, receipts and spending.

Reserve the offered amount when an errand is successfully created. Transfer reserved credits to the courier when the errand is successfully completed. Release credits when an errand is cancelled or expires before completion, subject to clarification of cancellation rules. Prevent negative available balances and unsupported credit movements.

### Asynchronous communication — FR5

**S1:** publish events for created, accepted, delivered, cancelled and expired errands. Notify couriers when they have successfully been assigned an errand.

**S2:** notify users about relevant status changes when events are received. The communication and reliability mechanisms remain design decisions; the backlog does not mandate an outbox.

## Quality requirements

| Area | Backlog target | Planned iteration |
| --- | --- | --- |
| Password protection — NFR2 | Salted password hashes; no plaintext passwords in storage, logs, events, errors, analytics or backups; redact credential fields | S1 |
| Credit consistency — NFR3 | Each transfer completes fully or makes no balance change; repeated requests/events cannot duplicate transfers; failed transfers preserve the combined balance | S1 |
| Deployability — NFR5 | Documented container deployment on a clean machine | S1 |
| Logging — NFR6 | Application errors and significant domain events include timestamp, service, severity and description; no credentials | S1 |
| API performance — NFR1 | At least 95% of API requests within 2 seconds with up to 100 active concurrent users, including order actions and filtered supplier queries | S2 |
| Transfer performance — NFR1 | Under 100 concurrent errand completions, 95% of transfers update both balances within 2 seconds | S2 |
| History performance — NFR1 | Retrieve each user's 30 most recent transactions within 5 seconds under 100 concurrent requests | S2 |
| Responsive UX — NFR4 | Usable desktop/mobile layouts; core functions without horizontal scrolling in supported viewports | S2 |
| Message recovery — NFR7 | Retry after temporary broker failures, avoid silently losing acknowledged business events and safely handle duplicate delivery | S2 |

Responsive UX is also a mandatory project outcome in the original brief. S2 is the backlog's planned timing, not an exemption from that requirement.

## Selected nice-to-haves

| Feature | Backlog scope | Planned iteration |
| --- | --- | --- |
| Proximity sorting — NTH1 | Courier pins a current location; requests sort by pickup distance and show estimated distance | S3 |
| Dynamic pricing — NTH2 | Recommend a credit reward considering courier demand, item count, distance and urgency | S3 |
| Reviews — NTH3 | Post-completion numerical ratings, overall courier rating and admin flags for repeated poor reviews | S3 |
| Review extensions — NTH3 | Optional written reviews and rating-aware selection during simultaneous courier acceptance | S4 |
| Cloud deployment — NTH4 | Remote access for authorised users and persistent centralised data | S2 |
| Cloud updates — NTH4 | Deploy application updates without users installing updates themselves | S3 |

These are selected backlog items, not completed features. Ownership and appropriate contribution by every member remain to be assigned.

## Deployment strategy

**Local development:** retain a complete containerised local setup so development, integration and demonstrations can proceed without the school AWS account. Local deployment is sufficient for the brief's mandatory deployment scope.

**AWS deployment:** host application services on EC2, as chosen by the team. The intent is to deploy the same application to a shared cloud environment with deployment-specific configuration and separate data. EC2 instance count, sizing, network layout, ingress tooling and operational setup are not yet decided. ECS/Fargate is not the selected application hosting approach.

**Supporting services:** database, broker and email placement/providers remain open. Choosing EC2 for application services does not require putting every supporting component on the same instance. Evaluate cost and school-account restrictions before deciding on RDS or other managed services.

**Readiness:** confirm account access, budget and permitted services before finalising infrastructure. Agree responsibility for secure access, secrets, persistent data, backups, logs, server maintenance and recovery. Keep local and shared cloud data separate. These are deployment concerns to resolve, not a provisioned setup.

**Delivery sequence:** establish the local application first, target cloud deployment in S2 as recorded in the backlog, and validate a small working journey on EC2 once access becomes available. User Service implementation was separately authorised; no cloud resources have been provisioned.

## Decisions still needed

- Confirm User Service's documented defaults with teammates, especially accepted school domains and session/rate-limit policies. First-admin provisioning uses an operator command for a verified account; Supplier can use this in S1.
- Database hosting, other services' database choices, message broker, production email provider and full-team container deployment. **Outbox remains undecided; Credit integration is deferred**, including recovery and eventual onboarding of accounts verified before integration exists.
- Cancellation eligibility, expiry duration and post-pickup penalty rules. Reconcile FR3.6.1's penalty with FR4.1.6's release rule, and clarify the release-on-completion wording in FR4.1.7.
- Physical quantity limits, supported pickup-location choices, and the meaning of S1 assignment notification versus S2 general notifications.
- Behaviour of rating-based simultaneous acceptance, and ownership of frontend, integration, deployment and nice-to-have work.
- Backlog cleanup: duplicated FR3.3.3 numbering, overlapping NFR7.1/7.4 retry wording, and mockup alignment with specified verification expiry and initial credits.

## Sources

- `CS3219-ProjectDescription.pdf`, supplied by the user: required services, scope boundaries, deployment baseline and contribution expectations, particularly pages 2–6.
- `Project-D1-Template.docx`, supplied by the user: functional/non-functional requirements, selected nice-to-haves and S1–S4 allocation. The summaries here paraphrase those tables.
- Team discussion: Next.js/TypeScript and Go stack direction; EC2 application hosting; own email verification; outbox explicitly not finalised.

The source attachments are not stored in this repository. This brief records requirements and decisions; service-specific implementation details belong in the service documentation.
