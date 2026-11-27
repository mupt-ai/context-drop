#!/usr/bin/env bash
# Build the server image and deploy it to Cloud Run, the home of contextdrop.dev.
#
#   deploy/cloudrun/deploy.sh
#
# State lives in Cloud SQL (Postgres), so servers come and go freely: a deploy starts new ones, and
# daemons reconnect within seconds. Secrets come from Secret Manager; Slack and Linear are on only
# when their client ID secrets exist.
set -euo pipefail

PROJECT=${PROJECT:-context-drop}
REGION=${REGION:-us-central1}
SERVICE=${SERVICE:-context-drop}
SQL=${SQL:-context-drop:us-central1:context-drop}
MAX=${MAX:-2} # each server holds up to 6 database connections; db-f1-micro allows 25, and a deploy runs old and new servers at once
here=$(cd "$(dirname "$0")" && pwd)
tag=$(git -C "$here" rev-parse --short HEAD)
image="$REGION-docker.pkg.dev/$PROJECT/context-drop/server:$tag"

gcloud builds submit --project "$PROJECT" --region "$REGION" --tag "$image" "$here/../../cli"

secrets="CONTEXT_DROP_DATABASE_URL=context-drop-database-url:latest,CONTEXT_DROP_ADMIN_TOKEN=context-drop-admin-token:latest"
has() { gcloud secrets describe "$1" --project "$PROJECT" >/dev/null 2>&1; }
if has context-drop-slack-client-id; then
	for s in client-id:CLIENT_ID client-secret:CLIENT_SECRET signing-secret:SIGNING_SECRET key:KEY; do
		secrets+=",CONTEXT_DROP_SLACK_${s#*:}=context-drop-slack-${s%%:*}:latest"
	done
fi
if has context-drop-linear-client-id; then
	for s in client-id:CLIENT_ID client-secret:CLIENT_SECRET webhook-secret:WEBHOOK_SECRET; do
		secrets+=",CONTEXT_DROP_LINEAR_${s#*:}=context-drop-linear-${s%%:*}:latest"
	done
fi

gcloud run deploy "$SERVICE" --project "$PROJECT" --region "$REGION" --image "$image" \
	--service-account "context-drop-server@$PROJECT.iam.gserviceaccount.com" \
	--add-cloudsql-instances "$SQL" \
	--allow-unauthenticated --port 8080 --cpu 1 --memory 512Mi --no-cpu-throttling \
	--min-instances 1 --max-instances "$MAX" --concurrency 1000 --timeout 3600 --session-affinity \
	--env-vars-file "$here/env.yaml" \
	--set-secrets "$secrets"
