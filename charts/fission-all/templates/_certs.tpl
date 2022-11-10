

{{- define "fision.selfSignedCABundleCertPEM" -}}
  {{- $caKeypair := .selfSignedCAKeypair | default (genCA "fission-ca" 1825) -}}
  {{- $_ := set . "selfSignedCAKeypair" $caKeypair -}}
  {{- $caKeypair.Cert -}}
{{- end -}}

{{- define "webhook.caBundleCertPEM" -}}
  {{- if .Values.webhook.caBundlePEM -}}
    {{- trim .Values.webhook.caBundlePEM -}}
  {{- else -}}
    {{- $caKeypair := .selfSignedCAKeypair | default (genCA "fission-ca" 1825) -}}
    {{- $_ := set . "selfSignedCAKeypair" $caKeypair -}}
    {{- $caKeypair.Cert -}}
  {{- end -}}
{{- end -}}

{{- define "webhook.certPEM" -}}
  {{- if .Values.webhook.crtPEM -}}
    {{- trim .Values.webhook.crtPEM -}}
  {{- else -}}
    {{- $webhookName := printf "%s.%s.svc" (include "fission.svc" .) .Release.Namespace }}
    {{- $fullWebhookName := printf "%s.%s.svc.cluster.local" (include "fission.svc" .) .Release.Namespace -}}
    {{- $webhookCA := required "self-signed CA keypair is requried" .selfSignedCAKeypair -}}
    {{- $webhookServerTLSKeypair := .webhookTLSKeypair | default (genSignedCert $webhookName nil (list $webhookName $fullWebhookName) 1825 $webhookCA) }}
    {{- $_ := set . "webhookTLSKeypair" $webhookServerTLSKeypair -}}
    {{- $webhookServerTLSKeypair.Cert -}}
  {{- end -}}
{{- end -}}

{{- define "webhook.keyPEM" -}}
  {{- if .Values.webhook.keyPEM -}}
    {{ trim .Values.webhook.keyPEM }}
  {{- else -}}
    {{- $webhookName := printf "%s.%s.svc" (include "fission.svc" .) .Release.Namespace -}}
    {{- $fullWebhookName := printf "%s.%s.svc.cluster.local" (include "fission.svc" .) .Release.Namespace -}}
    {{- $webhookCA := required "self-signed CA keypair is requried" .selfSignedCAKeypair -}}
    {{- $webhookServerTLSKeypair := .webhookTLSKeypair | default (genSignedCert $webhookName nil (list $webhookName $fullWebhookName) 1825 $webhookCA) -}}
    {{- $_ := set . "webhookTLSKeypair" $webhookServerTLSKeypair -}}
    {{- $webhookServerTLSKeypair.Key -}}
  {{- end -}}
{{- end -}}
