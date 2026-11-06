"""Check local and EC2 Pricing boundaries without printing resolved secrets."""
import json
import os
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent.parent
env = {**os.environ, 'ECR_REGISTRY': 'test.invalid', 'FOC_IMAGE_TAG': 'a' * 40}
for override, local in [('compose.pricing.yaml', True), ('compose.ec2.yaml', False)]:
    config = json.loads(subprocess.check_output(
        ['docker', 'compose', '-f', 'compose.yaml', '-f', override, '--profile', 'test', 'config', '--format', 'json'], cwd=root, env=env))
    services = config['services']
    pricing = services['pricing-service']
    database = services['pricing-db']
    assert not pricing.get('ports'), 'Pricing must remain private'
    assert not database.get('ports'), 'Pricing database must remain private'
    assert pricing['read_only'] and pricing['cap_drop'] == ['ALL']
    assert 'no-new-privileges:true' in pricing['security_opt']
    assert pricing['healthcheck']['test'][1] == 'python'
    assert pricing['depends_on']['order-service']['condition'] == 'service_started'
    assert services['gateway']['depends_on']['pricing-service']['condition'] == 'service_healthy'
    assert services['smoke']['environment']['SMOKE_PRICING'] == '1'
    order_env, price_env = services['order-service']['environment'], pricing['environment']
    assert order_env['ORDER_PRICING_TOKEN'] == price_env['PRICING_QUOTE_TOKEN']
    assert order_env['ORDER_PRICING_SOURCE_TOKEN'] == price_env['PRICING_ORDER_TOKEN']
    assert price_env['PRICING_SUPPLIER_TOKEN'] == services['supplier-service']['environment']['SUPPLIER_INTERNAL_TOKEN']
    assert price_env['PRICING_ORDER_URL'] == 'http://order-service:8081'
    assert price_env['PRICING_SOURCE_PROVENANCE'] == 'observed'
    assert '/opt/local-demo' not in price_env.get('PRICING_PACKAGE_DIR', '')
    assert any(volume['source'] == 'pricing-data' for volume in database['volumes'])
    if local:
        assert pricing['build']['target'] == 'local-demo'
        assert pricing['build']['context'] == str(root / 'order_pricing')
    else:
        assert 'build' not in pricing
        assert pricing['image'] == 'test.invalid/foc/pricing-service:' + 'a' * 40
        assert pricing['restart'] == database['restart'] == 'unless-stopped'
        assert pricing['logging']['options']['max-size'] == '10m'
        assert database['logging']['options']['max-size'] == '10m'
    print('PASS: private Pricing, source credentials, storage and readiness in ' + override)
