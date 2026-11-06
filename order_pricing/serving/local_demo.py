"""Build a LOCAL-ONLY policy with synthetic distances for catalogue UI testing.

The ordering and 150 m steps are arbitrary test data, not campus geography.
This package must never be selected by production image publishing.
"""
import json
from pathlib import Path
import tempfile

from formula.build import build

root = Path('/app')
fixture = json.loads((root / 'tests/fixtures/policy-input.json').read_text())
traffic = json.loads((root / 'formula/data/foot_traffic.json').read_text())
locations = [item['locationId'] for item in traffic['locations']]
fixture['context']['catalogue']['locations'] = [{'id': value} for value in locations]
fixture['request']['delivery']['id'] = 'central-library'
fixture['resolvedPlaces']['deliveryLocationId'] = 'central-library'
fixture['context']['routes'] = [
    {'pickupLocationId': pickup, 'deliveryLocationId': delivery,
     'walkableMetres': 0 if pickup == delivery else 150 * (1 + abs(i-j))}
    for i, pickup in enumerate(locations) for j, delivery in enumerate(locations)
]
fixture['context']['sources'][2]['snapshotId'] = 'local-demo-synthetic-routes-001'
with tempfile.TemporaryDirectory() as directory:
    path = Path(directory) / 'fixture.json'
    path.write_text(json.dumps(fixture))
    # The source checksum in the generated manifest identifies the actual policy.
    base = json.loads((root / 'formula/releases/effort-traffic-001/manifest.json').read_text())
    build('/opt/local-demo', base['provenance']['codeCommit'],
          'local-demo-synthetic-routes-001', fixture=path)
