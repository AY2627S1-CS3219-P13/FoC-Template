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
The override currently exists on EC2 and still needs to be included in the
versioned deployment configuration for CI/CD.

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
  existing deployment data. No permanent fix has been applied.

## Next infrastructure work

- Include the verified restart settings in the versioned deployment configuration.
- Set up CI/CD, then add public HTTPS access through an ALB.
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
