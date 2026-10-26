{{/*
Expand the name of the chart.
*/}}
{{- define "offer-availability.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "offer-availability.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "offer-availability.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "offer-availability.labels" -}}
helm.sh/chart: {{ include "offer-availability.chart" . }}
{{ include "offer-availability.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels. Every Deployment and Service ALSO pins
app.kubernetes.io/component (api), so a component added later cannot be
selected by accident.
*/}}
{{- define "offer-availability.selectorLabels" -}}
app.kubernetes.io/name: {{ include "offer-availability.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "offer-availability.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "offer-availability.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding DATABASE_URL: the operator's own
(database.existingSecret) or the one this chart creates from database.url.
*/}}
{{- define "offer-availability.databaseSecretName" -}}
{{- if .Values.database.existingSecret }}
{{- .Values.database.existingSecret }}
{{- else }}
{{- include "offer-availability.fullname" . }}-database
{{- end }}
{{- end }}

{{/*
Image reference. An empty tag is "v" + appVersion: the release job tags the
published image v<version> (and the chart appVersion is <version>).
*/}}
{{- define "offer-availability.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default (printf "v%s" .Chart.AppVersion) }}
{{- end }}

{{/*
MANDATORY: cmd/api exits at boot without DATABASE_URL; there is no in-memory
mode. Fails the render naming the values instead of crash-looping.
*/}}
{{- define "offer-availability.requireDatabase" -}}
{{- if not (or (trim (toString .Values.database.url)) (trim (toString .Values.database.existingSecret))) -}}
{{- fail "offer-availability requires database.url or database.existingSecret to be set (DATABASE_URL): the service runs on real Postgres only and exits at boot without it." -}}
{{- end -}}
{{- end -}}

{{/*
MANDATORY: cmd/api exits at boot without KAFKA_BROKERS (a list of blanks is as
missing as an empty value).
*/}}
{{- define "offer-availability.requireKafka" -}}
{{- if not (trim (replace "," " " (toString .Values.kafka.brokers))) -}}
{{- fail "offer-availability requires kafka.brokers to be set (KAFKA_BROKERS, comma-separated host:port list): the service runs on real Kafka only and exits at boot without it." -}}
{{- end -}}
{{- end -}}

{{/*
The node-topology consumer always runs, under a STABLE consumer group id that
must come from values (cmd/api exits at boot without
NODE_TOPOLOGY_CONSUMER_GROUP). Stable = a rescheduled pod resumes from its
committed offsets; never derive it from the pod name.
*/}}
{{- define "offer-availability.requireConsumerGroups" -}}
{{- range $c := list (dict "key" "nodeTopologyConsumerGroup" "env" "NODE_TOPOLOGY_CONSUMER_GROUP") -}}
{{- if not (trim (toString (index $.Values.config $c.key))) -}}
{{- fail (printf "config.%s is required (%s): the consumer always runs and cmd/api exits at boot without a group. Use a STABLE, service-prefixed id, never the pod name." $c.key $c.env) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
extraEnv may not bring back an in-memory, log or permissive mode: no *_MODE
variable at all (the service has none; the binary is real Postgres + Kafka
only). Called with the list to check and the name of its value.
*/}}
{{- define "offer-availability.requireNoModeEnv" -}}
{{- $where := .where -}}
{{- range .list -}}
{{- $name := toString .name -}}
{{- if hasSuffix "_MODE" $name -}}
{{- fail (printf "%s sets %s - *_MODE variables do not exist here: offer-availability runs on real Postgres and real Kafka only, with no in-memory, log, permissive or off mode." $where $name) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
extraEnv may not set a variable the chart renders itself from otel.* and
environment (the same name would be set twice). Called with the list to check
and the name of its value.
*/}}
{{- define "offer-availability.requireNoOwnedEnv" -}}
{{- $where := .where -}}
{{- $owned := list "ENVIRONMENT" "SERVICE_VERSION" "OTEL_SERVICE_NAME" "OTEL_EXPORTER_OTLP_ENDPOINT" -}}
{{- range .list -}}
{{- $name := toString .name -}}
{{- if has $name $owned -}}
{{- fail (printf "%s sets %s - the chart renders it from otel.* / environment / image.tag; set the value instead." $where $name) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
SHUTDOWN_DRAIN_DELAY in whole seconds (rounded up), for the grace-period
check. Accepts "0", "<n>s", "<n>m" and "<n>ms"; empty is the binary's default
(5s). Anything else fails the render naming the value.
*/}}
{{- define "offer-availability.drainSeconds" -}}
{{- $d := trim (toString .Values.config.shutdownDrainDelay) -}}
{{- if eq $d "" -}}5
{{- else if eq $d "0" -}}0
{{- else if regexMatch "^[0-9]+ms$" $d -}}{{ div (add (int (trimSuffix "ms" $d)) 999) 1000 }}
{{- else if regexMatch "^[0-9]+s$" $d -}}{{ int (trimSuffix "s" $d) }}
{{- else if regexMatch "^[0-9]+m$" $d -}}{{ mul (int (trimSuffix "m" $d)) 60 }}
{{- else -}}
{{- fail (printf "config.shutdownDrainDelay is %q - want \"0\" or a whole number of ms, s or m (for example \"5s\"): the chart adds it to the shutdown budget it checks terminationGracePeriodSeconds against." $d) -}}
{{- end -}}
{{- end -}}

{{/*
The graceful shutdown is sequential: SHUTDOWN_DRAIN_DELAY + HTTP drain (10s) +
the loops (relay, consumer, janitor; cancelled together, awaited up to 10s) =
drain + 20s. The grace period must exceed that with a margin (drain + 25s,
30s at the default 5s drain): a shorter one SIGKILLs the loops mid-drain,
stranding an uncommitted offset or an in-flight outbox batch.
*/}}
{{- define "offer-availability.requireGracePeriod" -}}
{{- $drain := int (include "offer-availability.drainSeconds" .) -}}
{{- $min := add $drain 25 -}}
{{- if lt (int .Values.terminationGracePeriodSeconds) (int $min) -}}
{{- fail (printf "terminationGracePeriodSeconds is %v - want at least %v (SHUTDOWN_DRAIN_DELAY %vs + HTTP drain 10s + loops 10s + margin): the loops drain after the HTTP server and a shorter grace period SIGKILLs them mid-drain." .Values.terminationGracePeriodSeconds $min $drain) -}}
{{- end -}}
{{- end -}}

{{/*
Every render-time requirement of the real runtime, in one include. The api
Deployment includes it, so a mis-set release fails `helm template` /
`helm install`.
*/}}
{{- define "offer-availability.requireRealRuntime" -}}
{{- include "offer-availability.requireDatabase" . -}}
{{- include "offer-availability.requireKafka" . -}}
{{- include "offer-availability.requireConsumerGroups" . -}}
{{- include "offer-availability.requireNoModeEnv" (dict "list" .Values.extraEnv "where" "extraEnv") -}}
{{- include "offer-availability.requireNoOwnedEnv" (dict "list" .Values.extraEnv "where" "extraEnv") -}}
{{- include "offer-availability.requireGracePeriod" . -}}
{{- end -}}
