"""Validate root Compose wiring without printing resolved credentials."""
import json
import os
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent.parent
env_file = root / '.env'
original = env_file.read_text()
try:
    for origin, secure in [('http://localhost:3000', 'false'), ('https://review.example', 'true')]:
        lines = original.splitlines()
        for key, value in [('USER_APP_ORIGIN', origin), ('USER_COOKIE_SECURE', secure)]:
            lines = [line for line in lines if not line.startswith(key + '=')]
            lines.append(key + '=' + value)
        env_file.write_text('\n'.join(lines) + '\n')
        env = dict(os.environ)
        for key in ['USER_APP_ORIGIN', 'USER_COOKIE_SECURE', 'CREDIT_SESSION_COOKIE']:
            env.pop(key, None)
        config = json.loads(subprocess.check_output(['docker', 'compose', 'config', '--format', 'json'], cwd=root, env=env))
        services = config['services']
        assert services['user-service']['environment']['USER_APP_ORIGIN'] == origin
        assert services['credit-service']['environment']['CREDIT_APP_ORIGIN'] == origin
        assert services['order-service']['environment']['ORDER_APP_ORIGIN'] == origin
        # Empty tells both Go loaders to derive the cookie from the origin.
        assert not services['credit-service']['environment']['CREDIT_SESSION_COOKIE']
        assert not services['order-service']['environment']['ORDER_SESSION_COOKIE']
        assert services['supplier-service']['environment']['USER_COOKIE_SECURE'] == secure
        print('PASS: shared origin and automatic cookie selection for ' + origin)
finally:
    env_file.write_text(original)
