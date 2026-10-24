{{/*
Fully qualified name of the MCP server (ADR 0006): its own Deployment and
ClusterIP Service, deliberately separate from the api (component=mcp vs
component=api), so the read-only MCP surface scales and fails independently
of the OLTP loop.
*/}}
{{- define "offer-availability.mcpFullname" -}}
{{- include "offer-availability.fullname" . }}-mcp
{{- end }}
