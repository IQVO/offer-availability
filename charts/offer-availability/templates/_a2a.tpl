{{/*
Fully qualified name of the A2A agent (ADR 0010): its own Deployment and
ClusterIP Service, deliberately separate from the api and the MCP server
(component=a2a), so the read-only A2A surface scales and fails independently
of the OLTP loop.
*/}}
{{- define "offer-availability.a2aFullname" -}}
{{- include "offer-availability.fullname" . }}-a2a
{{- end }}

{{/* agents.colocate (warehouse-infra ADR 0012): "true" iff colocate, mcp and a2a are all on; else empty. */}}
{{- define "offer-availability.agentsColocated" -}}
{{- if and (.Values.agents | default dict).colocate .Values.mcp.enabled .Values.a2a.enabled }}true{{ end -}}
{{- end }}

{{- define "offer-availability.agentsFullname" -}}
{{- printf "%s-agents" (include "offer-availability.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
