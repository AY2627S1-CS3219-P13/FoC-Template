# AWS deployment notes

The initial AWS deployment was confirmed working by Keith on 24 October 2026.
The application runs on one Ubuntu 24.04 `t3a.medium` EC2 instance in Singapore,
using Docker Compose and an encrypted 40 GiB gp3 root volume. Browser access is
through SSM tunnels to the gateway on port 3000 and Mailpit on port 8025.
All four service readiness endpoints passed after the recovery below.

The server's `compose.override.yaml` now sets `restart: unless-stopped` on all
11 runtime containers. A controlled EC2 reboot passed: Docker and all containers
started automatically, all four backend readiness endpoints returned ready, and
the gateway returned HTTP 200. User, Supplier and Order each reported one
automatic container restart after boot. Startup robustness remains a follow-up;
recovery after a reboot does not validate the fresh-volume initialization path.
The earlier override exists on EC2. The versioned `compose.ec2.yaml` now carries
these restart settings for image deployments; the deployment command explicitly
selects it instead of that untracked server override.

## Pending first start issue

On the first deployment, Supplier and Order exited with `database migration
failed`. Their PostgreSQL health checks passed during initialization, before
the temporary database servers shut down and the final TCP listeners started.
The application startup timestamps overlap that initialization window, indicating
a database readiness race. The homepage still returned HTTP 200 while those two
services were stopped.

- **Reproduction condition:** Start the stack against fresh, empty database
  volumes. A dependent service starts after the health check passes for the
  temporary PostgreSQL server but before the final server accepts TCP connections.
  Timing affects reproduction; an ordinary restart with initialized volumes may
  not reproduce it.
- **Intended behavior:** Services start only once their application databases
  accept connections over the same TCP path the services use. Deployment success
  requires every backend to be ready, in addition to the homepage loading.
- **Recovery performed:** After database initialization completed, ran
  `docker compose start supplier-service order-service`. Supplier applied all
  3 migrations and Order applied all 9; both then returned ready.
- **Follow-up:** Correct database readiness checks, review bounded startup
  retries, and make deployment verification check all four backends. Confirm
  the fix with repeated fresh starts using disposable test volumes; preserve
  existing deployment data. The EC2 override now probes TCP at `127.0.0.1`,
  excluding the temporary socket-only initialization server. The original
  development Compose health checks remain unchanged. CI exercises fresh
  databases with the EC2 override in its disposable stack.

## Next infrastructure work

- Verify a manual deployment of published images with `scripts/deploy-ec2.sh`.
- Connect the deployment command to GitHub Actions, then add public HTTPS through an ALB.
- Measure runtime memory before attempting to downsize to `t3a.small`.

## GitHub AWS connection check

The `AWS connection check` workflow is a manual infrastructure check. It assumes
the GitHub deployment role through OIDC, logs in to ECR, reads image metadata
from the six application repositories, and sends a read-only readiness command
to EC2 through SSM. Empty image lists are expected before the first image upload.
It does not upload images, change application data or restart containers.

The role trust policy accepts this repository's immutable OIDC subject on `main`
only. The workflow therefore must be merged into `main` before it can be run.
It does not use a GitHub environment; adding one later requires adjusting the
OIDC subject condition to match the environment identity.

Configure these repository variables under Settings, Secrets and variables,
Actions, Variables:

| Variable | Value to copy from AWS |
| --- | --- |
| `AWS_REGION` | Deployment region, currently `ap-southeast-1` |
| `AWS_ROLE_ARN` | ARN of the `foc-github-deploy` IAM role |
| `ECR_REGISTRY` | Private registry hostname, without `https://` or a repository path |
| `EC2_INSTANCE_ID` | ID of the running application instance |

The workflow also restricts authentication to the current school AWS account.
When moving to another account, update that account restriction along with the
variables and IAM policies. Application secrets remain on EC2.

After merging, open GitHub Actions, select AWS connection check, choose Run
workflow with branch `main`, and inspect each step. Success verifies AWS
authentication, ECR login and repository reads, SSM command execution, and current
backend readiness. Image upload and EC2 image download permissions still need
to be exercised by the subsequent image build and deployment workflow.

The connection check passed on 24 October 2026, including GitHub OIDC login,
ECR authentication and repository reads, and all backend readiness requests
through SSM.

## Publish application images

`Publish AWS images` is a manual workflow on `main`. It first requires the latest
push or manual CI run for the selected commit to have completed successfully.
The selected commit is the `main` revision captured when the publisher starts;
later changes to `main` do not change the source being built. If CI did not run
because Actions was disabled, run CI manually on `main` before publishing.

The publisher builds the four Go backends, frontend and gateway using their
existing Dockerfiles on GitHub runners. It builds Linux amd64 images for the
current EC2 architecture and pushes them to the corresponding `foc/` ECR
repositories. Every image uses the full Git commit SHA as its immutable tag.
Runtime secrets are not supplied to the build; application `.env` files stay on
EC2. PostgreSQL and Mailpit continue using upstream images.

To publish:

1. Merge the workflow into `main` and wait for that commit's CI run to pass.
2. Open Actions, select Publish AWS images, and choose Run workflow on `main`.
3. Check all six matrix jobs complete successfully. Their summaries contain the
   image references; the same commit tag should appear in all six ECR repositories.

The publisher reuses tags that already exist, allowing a failed release to be
retried without overwriting immutable images. Permission errors and missing
repositories fail the workflow. A partial failure may leave only some images
uploaded; do not deploy a commit until all six jobs succeed. Concurrent publisher
runs are serialized, and at most two images build in parallel within a run.

Publishing only stores images. It does not connect to EC2 or change the running
application. The next deployment checkpoint must configure EC2 image downloads,
use the versioned restart settings, verify all four backends after deployment,
and retain the existing database volumes and credentials.

## Deploy published images to EC2

EC2 uses the ECR Docker credential helper and its existing instance role to
download private images. Install `amazon-ecr-credential-helper` on Ubuntu and
configure `/root/.docker/config.json` with a `credHelpers` entry mapping the
private registry hostname to `ecr-login`. The root configuration applies because
Docker commands run with `sudo`. The AWS CLI and personal AWS credentials are
not needed on EC2. The gateway image pull was confirmed working before this
deployment checkpoint.

The image deployment command combines `compose.yaml` with `compose.ec2.yaml`.
The latter removes runtime build definitions, references the six ECR images by
one full commit tag, retains restart policies, and checks database readiness
over TCP. The root project name and existing database volume definitions stay
the same. The earlier server `compose.override.yaml` is not selected by this
command. Local development still uses `scripts/start.sh` and local builds.

On the existing EC2 instance, after merging the deployment changes:

```sh
cd /opt/foc
sudo git pull --ff-only origin main
sudo env ECR_REGISTRY=291723765178.dkr.ecr.ap-southeast-1.amazonaws.com \
  sh scripts/deploy-ec2.sh ac4c2d0349e79d51069e92033bcfab69106cf16e
```

The example tag is the first successfully published release. For future changes,
use a successfully published commit and a compatible checkout of the deployment
configuration. All six repositories must contain the selected commit tag.
The command preserves the existing `.env` files; it does not generate replacement
credentials or delete database volumes.

The command performs these steps:

1. Validate the arguments, existing environment files and combined Compose model.
2. Acquire a deployment lock and download any of the six images not already cached.
   Download failure stops the deployment before running services are stopped.
3. Stop the old Order process, as required for Order upgrades.
4. Update User, Supplier and Credit and wait for all three readiness endpoints.
   This ensures the required Credit endpoint is available before starting Order.
5. Update Order, frontend and gateway, check Order readiness and the homepage,
   and confirm all six containers reference the requested images.

Single-instance deployment briefly interrupts service. A rollout failure returns
a nonzero status and may leave a partially updated stack for investigation; the
script does not automatically reverse database migrations. Retrying the command
uses the same immutable images and existing data. Reboot recovery remains handled
by Docker's restart policies; restarting EC2 does not fetch a newer application
version. A successful pull alone also does not replace an existing container.

Deployment checks include command-order/failure tests and a fresh disposable CI
stack that uses locally tagged images with the EC2 override. It runs the same
deployment command, the existing smoke check and the funded four-service lifecycle
without private ECR access. This CI check removes only its own disposable volumes;
do not run `scripts/check-ec2-images.sh` on the application server.
