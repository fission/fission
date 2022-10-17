apiVersion: admissionregistration.k8s.io/v1beta1
kind: ValidatingWebhookConfiguration
metadata:
  name: environment-validator-webhook
  labels:
    app: environment-validator-webhook
    kind: validating
webhooks:
  - name: environment-validator-webhook.slok.dev
    clientConfig:
      service:
        name: environment-validator-webhook
        namespace: default
        path: "/validating"
      caBundle: CA_BUNDLE
    rules:
      - operations: [ "CREATE", "UPDATE" ]
        apiGroups: ["extensions"]
        apiVersions: ["v1beta1"]
        resources: ["ingresses"]
        