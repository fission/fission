{{- define "deprecationWarnings" -}}
{{- $deprecations := list -}}

{{- if .Values.builderNamespace -}}
{{- $deprecations = append $deprecations "The 'builderNamespace' parameter is deprecated and will be removed in future release." -}}
{{- end -}}

{{- if .Values.functionNamespace -}}
{{- $deprecations = append $deprecations "The 'functionNamespace' parameter is deprecated and will be removed in future release." -}}
{{- end -}}

{{- if .Values.disableOwnerReference -}}
{{- $deprecations = append $deprecations "The 'disableOwnerReference' flag is temporary addition and will be removed in future release." -}}
{{- end -}}

{{- if $deprecations -}}
{{- range $deprecations }}
{{- printf "WARNING: %s" . | nindent 0 }}
{{- end -}}
{{- end -}}
{{- end -}}