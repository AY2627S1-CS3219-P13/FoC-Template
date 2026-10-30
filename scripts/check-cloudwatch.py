"""Validate observability wiring: bounded container logs and the agent config."""
import json
import os
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent.parent

# The agent must collect every Docker container log file as its own stream with
# bounded retention, plus memory and root-disk usage for the alarms.
agent = json.loads((root / 'scripts' / 'amazon-cloudwatch-agent.json').read_text())
files = agent['logs']['logs_collected']['files']['collect_list']
assert len(files) == 1, files
containers = files[0]
assert containers['file_path'] == '/var/lib/docker/containers/*/*-json.log'
assert containers['log_group_name'] == '/foc/app'
assert containers['log_stream_name'] == '{instance_id}'
# Without publish_multi_logs the agent ships only the most recently modified
# matching file, which would silently drop the other ten containers.
assert containers['publish_multi_logs'] is True
assert containers['retention_in_days'] == 7
metrics = agent['metrics']['metrics_collected']
assert 'mem_used_percent' in metrics['mem']['measurement']
assert 'used_percent' in metrics['disk']['measurement']
assert metrics['disk']['resources'] == ['/']
# Pin alarms to the instance without the hostname dimension.
assert agent['metrics']['append_dimensions']['InstanceId'] == '${aws:InstanceId}'
print('PASS: agent config collects container logs and memory/disk metrics, 7-day retention')

# Every service in the combined EC2 deployment model must use bounded
# json-file logging and publish its service label.
env = {**os.environ, 'ECR_REGISTRY': 'test.invalid', 'FOC_IMAGE_TAG': 'a' * 40}
config = json.loads(subprocess.check_output(
    ['docker', 'compose', '-f', 'compose.yaml', '-f', 'compose.ec2.yaml',
     'config', '--format', 'json'], cwd=root, env=env))
services = config['services']
for name, service in sorted(services.items()):
    logging_config = service['logging']
    options = logging_config['options']
    assert logging_config['driver'] == 'json-file', name
    assert options['max-size'] == '10m', name
    assert options['max-file'] == '3', name
    assert 'com.docker.compose.service' in options['labels'], name
print(f'PASS: bounded json-file logging with service labels on all {len(services)} EC2 containers')
