# Relative campus foot traffic: policy v1

This lookup is local to Pricing and bundled in each immutable policy release as
`artifacts/foot_traffic.json`. The editable source is
[`data/foot_traffic.json`](data/foot_traffic.json). It covers all 15 location IDs in
Supplier's current seed catalogue, which supplies Order's location choices. No
Supplier or Order service implementation changes are required. A later catalogue
addition without a score receives a neutral multiplier and `unknown_foot_traffic`;
unknown does not mean zero traffic. UTown is not in the current catalogue and is
not silently added to it by this policy.

The numbers below are **estimated policy assumptions**, not measured pedestrian
counts, density, independent people or available couriers. They express relative
activity at a shared lunch reference. Official NUS descriptions support facility
roles; they do not establish our numerical ranking or hourly percentages.

| Location ID | Label | Relative peak index |
| --- | --- | ---: |
| `blk-as8` | Blk AS8 | 80 |
| `com2` | COM2 | 65 |
| `com3` | COM3 | 85 |
| `central-library` | Central Library | 95 |
| `engineering-block-e3` | Engineering Block E3 | 60 |
| `engineering-block-e4` | Engineering Block E4 | 65 |
| `engineering-block-ea` | Engineering Block EA | 70 |
| `frontier` | Frontier | 100 |
| `hon-sui-sen-memorial-library` | Hon Sui Sen Memorial Library | 60 |
| `medicine-science-library` | Medicine+Science Library | 75 |
| `prince-george-s-park` | Prince George's Park | 80 |
| `terrace` | Terrace | 85 |
| `the-ridge` | The Ridge | 45 |
| `yusof-ishak-house` | Yusof Ishak House | 90 |
| `innovation4-0` | innovation4.0 | 35 |

Central Library is a central study/circulation destination; YIH is a student-life
hub. The Terrace is located at COM3, so these two IDs share a score/catchment rather
than being summed as independent crowds. Frontier serves the Science cluster;
PGP combines residential activity and shared amenities. Engineering/Computing
teaching corridors receive intermediate scores; the smaller research/food stops
receive lower assumptions. Sources: [Central Library](https://www.nus.edu.sg/nuslibraries/spaces/our-libraries/central-library),
[YIH](https://osa.nus.edu.sg/yih/),
[Terrace @ COM3](https://news.nus.edu.sg/not-just-for-dining-the-new-terrace-offers-common-spaces-e-sports-and-more/),
[Frontier](https://www.science.nus.edu.sg/student-life-and-services/wellness-academic-financial-support/pitstopscience/),
[PGP amenities](https://uci.nus.edu.sg/campus-life/housing-services/general-information-for-student-housing/facilities-amenities/),
[HSSML](https://nus.edu.sg/nuslibraries/spaces/our-libraries/hon-sui-sen-memorial-library),
[innovation4.0](https://nus.edu.sg/research/key-areas/smart-nation).

## Time profile

Use the calculation timestamp converted to Singapore time (UTC+08:00), never the
replica's local clock or the order acceptance deadline. Bands are half-open: at
14:00 the 14–16 band applies. The initial profile is shared by locations and days;
there is no inferred weekend, term-break, holiday, rain or event adjustment yet.
Residential/library evening patterns will need separate calibration.

| Singapore time | Fraction of relative peak activity |
| --- | ---: |
| 00:00–06:00 | 10% |
| 06:00–08:00 | 30% |
| 08:00–10:00 | 60% |
| 10:00–12:00 | 80% |
| 12:00–14:00 | 100% |
| 14:00–16:00 | 80% |
| 16:00–18:00 | 70% |
| 18:00–20:00 | 60% |
| 20:00–22:00 | 40% |
| 22:00–24:00 | 20% |

## Revised equation

The user-selected direction is lower traffic -> higher reward, capped at a 20%
premium before integer rounding and the final credit bounds:

```text
T = approachMinutes + pickupMinutes + walkableMetres / walkingMetresPerMinute
E = baseCredits + creditsPerHour * T / 60 + carryingAllowance(quantity)
F = pickupRelativePeakIndex / 100 * singaporeTimeFactor
M = 1 + footTrafficMaxPremium * (1 - F)
recommendedCredits = clamp(ceil(E * M), 5, 30)
```

`footTrafficMaxPremium` defaults to 0.20 and may be configured from 0 through 0.20.
Old configs omitting it default to zero. Unknown pickup scores use M=1, with an
explicit warning; they do not manufacture a supply deficit. Only pickup traffic is
used: no double-counting overlapping areas or adding pickup/delivery crowds. This
is a rough potential-courier proxy, not an observation of deliverers or capacity.
No open-request counts or fabricated supply ratios are used in this first rule.

Keep the existing effort coefficients (base 2, 15 credits/hour, 75 metres/minute,
5 approach minutes, 3 pickup minutes, 0.5 per extra item capped at 6). Unknown
quantity retains unknown diagnostics and no carrying allowance. Distance is still
a directed route-table lookup; the traffic table supplies no new distances.

For the synthetic 1,800 m/two-item example, E=10.5 credits and COM2's index is 65:

| Singapore time | F | M | Final credits |
| --- | ---: | ---: | ---: |
| 12:00 | 0.65 | 1.070 | 12 |
| 10:00 or 14:00 | 0.52 | 1.096 | 12 |
| 01:00 | 0.065 | 1.187 | 13 |

The 20% cap is on the unrounded multiplier; integer rounding can make the visible
change exceed 20% of a rounded earlier price. Normal 5–30 bounds still apply.
This rule does not guarantee acceptance or earnings. Crowds may also increase
demand or queues; that competing effect is not estimated here and requires data.

## Packaging, diagnostics and compatibility

`formula/releases/effort-traffic-001` is the new default release. The original
effort-only `formula/package` remains unchanged for replay/rollback. The new
release pins the lookup, coefficient and source hashes, with feature schema
`effort-traffic-1`. No shared HTTP or ContextSnapshot schema changes are needed.

The legacy shared `context.footfall[].estimatedPeople` field remains a people-count
projection with its original provenance. It is not repurposed as a relative index
and does not override this release-owned lookup. Feature records archive the lookup
dataset/version, reference time, peak index, local hour/time factor, effective index,
multiplier and adjusted credits. Quotes using the heuristic include
`estimated_supply`; the archived context can retain synthetic legacy counts that
this policy does not use. The release manifest makes the actual lookup retrievable.

The synthetic model still learns the effort core. Its deterministic policy applies
the same lookup multiplier to that core, and stale/missing Order context falls back
to the complete revised baseline. Synthetic evaluation metrics refer to core
distillation, not measured foot traffic or real price effectiveness. Model exports
pin the entire baseline manifest, including the lookup, across training/selection.
