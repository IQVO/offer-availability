{{/*
Fully qualified name of the frontend Module Federation remote
deployment/service (ADR 0007).

The remote (web/, `offer_availability_mfe`) is served by its own nginx pod
and reached through warehouse-infra's Nginx web gateway at
/mfes/offer-availability/. It is deliberately a separate workload from the
API: Kong never routes to it, and the api Service must never select it
(component=frontend vs component=api). Kept in its own helper file so the
frontend component stays self-contained (templates/frontend-*.yaml + this
file + the frontend: values block).
*/}}
{{- define "offer-availability.frontendFullname" -}}
{{- include "offer-availability.fullname" . }}-frontend
{{- end }}
