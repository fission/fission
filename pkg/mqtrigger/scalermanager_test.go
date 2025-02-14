package mqtrigger

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
)

func Test_toEnvVar(t *testing.T) {
	type args struct {
		str string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{"Empty string", args{""}, ""},
		{"Single word", args{"fission"}, "FISSION"},
		{"CamelCase", args{"responseTopic"}, "RESPONSE_TOPIC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toEnvVar(tt.args.str); got != tt.want {
				t.Errorf("toEnvVar() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getEnvVarlist(t *testing.T) {
	ctx := t.Context()
	// Kafka Test with Valid Secret
	pollingInterval := int32(30)
	cooldownPeriod := int32(300)
	minReplicaCount := int32(0)
	maxReplicaCount := int32(100)

	mqt := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "Test",
			Namespace: "default",
		},
		Spec: fv1.MessageQueueTriggerSpec{
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "test",
			},
			MessageQueueType: "kafka",
			Topic:            "topic",
			ResponseTopic:    "response-topic",
			ErrorTopic:       "error-topic",
			MaxRetries:       4,
			ContentType:      "application/json",
			PollingInterval:  &pollingInterval,
			CooldownPeriod:   &cooldownPeriod,
			MinReplicaCount:  &minReplicaCount,
			MaxReplicaCount:  &maxReplicaCount,
			Metadata: map[string]string{
				"bootstrapServers": "my-cluster-kafka-brokers.my-kafka-project.svc:9092",
				"consumerGroup":    "my-group",
				"topic":            "topic",
			},
			Secret:  "test-kafka-secrets",
			MqtKind: "keda",
		},
	}

	data := map[string][]byte{
		"authMode": []byte("sasl_plaintext"),
		"username": []byte("admin"),
		"password": []byte("admin"),
		"ca":       []byte("test_ca"),
		"cert":     []byte("test_cert"),
		"key":      []byte("test_key"),
	}
	namespace := apiv1.NamespaceDefault

	routerURL := "http://router.fission/fission-function"

	secret := &apiv1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-kafka-secrets",
			Namespace: namespace,
		},
		Data: data,
	}

	kubeClient := fake.NewSimpleClientset()
	_, err := kubeClient.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		assert.Equal(t, nil, err)
	}

	expectedEnvVars := []apiv1.EnvVar{
		{
			Name:  "TOPIC",
			Value: mqt.Spec.Topic,
		},
		{
			Name:  "HTTP_ENDPOINT",
			Value: "http://router.fission/fission-function/fission-function/test",
		},
		{
			Name:  "ERROR_TOPIC",
			Value: "error-topic",
		},
		{
			Name:  "RESPONSE_TOPIC",
			Value: "response-topic",
		},
		{
			Name:  "SOURCE_NAME",
			Value: "Test",
		},
		{
			Name:  "MAX_RETRIES",
			Value: "4",
		},
		{
			Name:  "CONTENT_TYPE",
			Value: "application/json",
		},
		{
			Name:  "BOOTSTRAP_SERVERS",
			Value: "my-cluster-kafka-brokers.my-kafka-project.svc:9092",
		},
		{
			Name:  "CONSUMER_GROUP",
			Value: "my-group",
		},
		{
			Name:  "TOPIC",
			Value: "topic",
		},
		{
			Name:  "KEY",
			Value: "test_key",
		},
		{
			Name:  "AUTH_MODE",
			Value: "sasl_plaintext",
		},
		{
			Name:  "USERNAME",
			Value: "admin",
		},
		{
			Name:  "PASSWORD",
			Value: "admin",
		},
		{
			Name:  "CA",
			Value: "test_ca",
		},
		{
			Name:  "CERT",
			Value: "test_cert",
		},
	}

	// Kafka Test with Invalid Secret Name
	mqt2 := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "Test2",
			Namespace: "default",
		},
		Spec: fv1.MessageQueueTriggerSpec{
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "test2",
			},
			MessageQueueType: "kafka",
			Topic:            "topic",
			ResponseTopic:    "response-topic",
			ErrorTopic:       "error-topic",
			MaxRetries:       4,
			ContentType:      "application/json",
			PollingInterval:  &pollingInterval,
			CooldownPeriod:   &cooldownPeriod,
			MinReplicaCount:  &minReplicaCount,
			MaxReplicaCount:  &maxReplicaCount,
			Metadata: map[string]string{
				"bootstrapServers": "my-cluster-kafka-brokers.my-kafka-project.svc:9092",
				"consumerGroup":    "my-group",
				"topic":            "topic",
			},
			Secret:  "test-kafka-secrets-invalid",
			MqtKind: "keda",
		},
	}

	// Test Code
	type args struct {
		mqt        *fv1.MessageQueueTrigger
		routerURL  string
		kubeClient kubernetes.Interface
	}
	tests := []struct {
		name    string
		args    args
		want    []apiv1.EnvVar
		wantErr bool
	}{
		{"Test kafka example", args{mqt, routerURL, kubeClient}, expectedEnvVars, false},
		{"Test kafka invalid secret", args{mqt2, routerURL, kubeClient}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getEnvVarlist(ctx, tt.args.mqt, tt.args.routerURL, tt.args.kubeClient)
			sort.Slice(got, func(i, j int) bool {
				return got[i].Name < got[j].Name
			})
			sort.Slice(tt.want, func(i, j int) bool {
				return tt.want[i].Name < tt.want[j].Name
			})
			if (err != nil) != tt.wantErr {
				t.Errorf("getEnvVarlist() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getEnvVarlist() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_checkAndUpdateTriggerFields(t *testing.T) {
	pollingInterval := int32(30)
	cooldownPeriod := int32(300)
	minReplicaCount := int32(0)
	maxReplicaCount := int32(100)

	// Test 1 with difference
	mqt := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "Test",
			Namespace: "default",
		},
		Spec: fv1.MessageQueueTriggerSpec{
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "test",
			},
			MessageQueueType: "kafka",
			Topic:            "topic",
			ResponseTopic:    "response-topic",
			ErrorTopic:       "error-topic",
			MaxRetries:       4,
			ContentType:      "application/json",
			PollingInterval:  &pollingInterval,
			CooldownPeriod:   &cooldownPeriod,
			MinReplicaCount:  &minReplicaCount,
			MaxReplicaCount:  &maxReplicaCount,
			Metadata: map[string]string{
				"bootstrapServers": "my-cluster-kafka-brokers.my-kafka-project.svc:9092",
				"consumerGroup":    "my-group",
				"topic":            "topic",
			},
			Secret:  "test-kafka-secrets",
			MqtKind: "keda",
		},
	}
	newMqt1 := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "Test",
			Namespace: "default",
		},
		Spec: fv1.MessageQueueTriggerSpec{
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "test2",
			},
			MessageQueueType: "kafka",
			Topic:            "my-topic",
			ResponseTopic:    "response-topic",
			ErrorTopic:       "error-topic",
			MaxRetries:       2,
			ContentType:      "application/json",
			PollingInterval:  &pollingInterval,
			CooldownPeriod:   &cooldownPeriod,
			MinReplicaCount:  &minReplicaCount,
			MaxReplicaCount:  &maxReplicaCount,
			Metadata: map[string]string{
				"bootstrapServers": "my-cluster-kafka-brokers-2.my-kafka-project.svc:9092",
				"consumerGroup":    "my-group-2",
				"topic":            "my-topic",
			},
			Secret:  "new-test-kafka-secrets",
			MqtKind: "keda",
		},
	}

	// Test 2 with no difference
	mqt2 := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "Test",
			Namespace: "default",
		},
		Spec: fv1.MessageQueueTriggerSpec{
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "test",
			},
			MessageQueueType: "kafka",
			Topic:            "topic",
			ResponseTopic:    "response-topic",
			ErrorTopic:       "error-topic",
			MaxRetries:       4,
			ContentType:      "application/json",
			PollingInterval:  &pollingInterval,
			CooldownPeriod:   &cooldownPeriod,
			MinReplicaCount:  &minReplicaCount,
			MaxReplicaCount:  &maxReplicaCount,
			Metadata: map[string]string{
				"bootstrapServers": "my-cluster-kafka-brokers.my-kafka-project.svc:9092",
				"consumerGroup":    "my-group",
				"topic":            "topic",
			},
			Secret:  "test-kafka-secrets",
			MqtKind: "keda",
		},
	}
	newMqt2 := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "Test",
			Namespace: "default",
		},
		Spec: fv1.MessageQueueTriggerSpec{
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "test",
			},
			MaxRetries:      4,
			PollingInterval: &pollingInterval,
			CooldownPeriod:  &cooldownPeriod,
			MinReplicaCount: &minReplicaCount,
			MaxReplicaCount: &maxReplicaCount,
			MqtKind:         "keda",
		},
	}
	type args struct {
		mqt    *fv1.MessageQueueTrigger
		newMqt *fv1.MessageQueueTrigger
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{"With diff", args{mqt, newMqt1}, true},
		{"With no diff", args{mqt2, newMqt2}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkAndUpdateTriggerFields(tt.args.mqt, tt.args.newMqt); got != tt.want {
				t.Errorf("checkAndUpdateTriggerFields() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getAuthTriggerSpec(t *testing.T) {
	ctx := t.Context()
	// Valid - with Secret
	pollingInterval := int32(30)
	cooldownPeriod := int32(300)
	minReplicaCount := int32(0)
	maxReplicaCount := int32(200)

	mqt1 := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "Test",
			Namespace: "default",
			UID:       "test123",
		},
		Spec: fv1.MessageQueueTriggerSpec{
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "test",
			},
			MessageQueueType: "kafka",
			Topic:            "topic",
			ResponseTopic:    "response-topic",
			ErrorTopic:       "error-topic",
			MaxRetries:       4,
			ContentType:      "application/json",
			PollingInterval:  &pollingInterval,
			CooldownPeriod:   &cooldownPeriod,
			MinReplicaCount:  &minReplicaCount,
			MaxReplicaCount:  &maxReplicaCount,
			Metadata: map[string]string{
				"bootstrapServers": "my-cluster-kafka-brokers.my-kafka-project.svc:9092",
				"consumerGroup":    "my-group",
				"topic":            "topic",
			},
			Secret:  "test-kafka-secrets",
			MqtKind: "keda",
		},
	}

	data := map[string][]byte{
		"authMode": []byte("sasl_plaintext"),
		"username": []byte("admin"),
		"password": []byte("admin"),
		"ca":       []byte("test_ca"),
		"cert":     []byte("test_cert"),
		"key":      []byte("test_key"),
	}

	namespace := apiv1.NamespaceDefault
	secret := &apiv1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-kafka-secrets",
			Namespace: namespace,
		},
		Data: data,
	}

	kubeClient := fake.NewSimpleClientset()
	_, err := kubeClient.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		assert.Equal(t, nil, err)
	}

	authenticationRef := fmt.Sprintf("%s-auth-trigger", mqt1.ObjectMeta.Name)

	blockOwnerDeletion := true
	expectedAuthTriggerObj := &kedav1alpha1.TriggerAuthentication{
		ObjectMeta: metav1.ObjectMeta{
			Name:      authenticationRef,
			Namespace: mqt1.ObjectMeta.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind:               "MessageQueueTrigger",
					APIVersion:         "fission.io/v1",
					Name:               mqt1.ObjectMeta.Name,
					UID:                mqt1.ObjectMeta.UID,
					BlockOwnerDeletion: &blockOwnerDeletion,
				},
			},
		},
		Spec: kedav1alpha1.TriggerAuthenticationSpec{
			SecretTargetRef: []kedav1alpha1.AuthSecretTargetRef{
				{
					Name:      mqt1.Spec.Secret,
					Parameter: "authMode",
					Key:       "authMode",
				},
				{
					Name:      mqt1.Spec.Secret,
					Parameter: "username",
					Key:       "username",
				},
				{
					Name:      mqt1.Spec.Secret,
					Parameter: "password",
					Key:       "password",
				},
				{
					Name:      mqt1.Spec.Secret,
					Parameter: "ca",
					Key:       "ca",
				},
				{
					Name:      mqt1.Spec.Secret,
					Parameter: "cert",
					Key:       "cert",
				},
				{
					Name:      mqt1.Spec.Secret,
					Parameter: "key",
					Key:       "key",
				},
			},
		},
	}

	// Invalid without secret
	mqt2 := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "Test",
			Namespace: "default",
			UID:       "test123",
		},
		Spec: fv1.MessageQueueTriggerSpec{
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "test",
			},
			MessageQueueType: "kafka",
			Topic:            "topic",
			ResponseTopic:    "response-topic",
			ErrorTopic:       "error-topic",
			MaxRetries:       4,
			ContentType:      "application/json",
			PollingInterval:  &pollingInterval,
			CooldownPeriod:   &cooldownPeriod,
			MinReplicaCount:  &minReplicaCount,
			MaxReplicaCount:  &maxReplicaCount,
			Metadata: map[string]string{
				"bootstrapServers": "my-cluster-kafka-brokers.my-kafka-project.svc:9092",
				"consumerGroup":    "my-group",
				"topic":            "topic",
			},
			Secret:  "test-kafka-no-secret",
			MqtKind: "keda",
		},
	}

	type args struct {
		mqt               *fv1.MessageQueueTrigger
		authenticationRef string
		kubeClient        kubernetes.Interface
	}
	tests := []struct {
		name    string
		args    args
		want    *kedav1alpha1.TriggerAuthentication
		wantErr bool
	}{
		{"With secret", args{mqt1, authenticationRef, kubeClient}, expectedAuthTriggerObj, false},
		{"With invalid secret", args{mqt2, authenticationRef, kubeClient}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getAuthTriggerSpec(ctx, tt.args.mqt, tt.args.authenticationRef, tt.args.kubeClient)
			if (err != nil) != tt.wantErr {
				t.Errorf("getAuthTriggerSpec() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.wantErr {
				return
			}

			gotSpec := got.Spec.SecretTargetRef
			//gotSpec := got.Object["spec"].(map[string]interface{})["secretTargetRef"].([]interface{})

			sort.Slice(gotSpec, func(i, j int) bool {
				return gotSpec[i].Parameter < gotSpec[j].Parameter
			})

			wantSpec := tt.want.Spec.SecretTargetRef

			sort.Slice(wantSpec, func(i, j int) bool {
				return wantSpec[i].Parameter < wantSpec[j].Parameter
			})

			if !reflect.DeepEqual(got.Kind, tt.want.Kind) &&
				!reflect.DeepEqual(got.APIVersion, tt.want.APIVersion) &&
				!reflect.DeepEqual(got.ObjectMeta, tt.want.ObjectMeta) &&
				!reflect.DeepEqual(gotSpec, wantSpec) {
				t.Errorf("getAuthTriggerSpec() = %v, want %v", got, tt.want)
			}

		})
	}
}
