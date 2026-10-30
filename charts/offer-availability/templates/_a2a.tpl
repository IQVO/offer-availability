{{/*
Fully qualified name of the A2A agent (ADR 0010): its own Deployment and
ClusterIP Service, deliberately separate from the api and the MCP server
(component=a2a), so the read-only A2A surface scales and fails independently
of the OLTP loop.
*/}}
{{- define "offer-availability.a2aFullname" -}}
{{- include "offer-availability.fullname" . }}-a2a
{{- end }}
